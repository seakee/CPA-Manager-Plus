import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    getRaw: vi.fn(),
    postForm: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    getRaw: mocks.getRaw,
    postForm: mocks.postForm,
    patch: mocks.patch,
    delete: mocks.delete,
  },
}));

import { authFilesApi } from './authFiles';
import { sha256RawTextHex } from '@/utils/apiKeyHash';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.getRaw.mockReset();
  mocks.postForm.mockReset();
  mocks.patch.mockReset();
  mocks.delete.mockReset();
});

describe('authFilesApi OAuth model alias normalization', () => {
  it('preserves force-mapping returned by CPA', async () => {
    mocks.get.mockResolvedValue({
      'oauth-model-alias': {
        codex: [
          {
            name: 'gpt-5-codex',
            alias: 'team-codex',
            fork: true,
            'force-mapping': true,
          },
        ],
      },
    });

    await expect(authFilesApi.getOauthModelAlias()).resolves.toEqual({
      codex: [
        {
          name: 'gpt-5-codex',
          alias: 'team-codex',
          fork: true,
          forceMapping: true,
        },
      ],
    });
  });

  it('serializes forceMapping using the CPA force-mapping field', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok' });

    await authFilesApi.saveOauthModelAlias('codex', [
      {
        name: 'gpt-5-codex',
        alias: 'team-codex',
        forceMapping: true,
      },
    ]);

    expect(mocks.patch).toHaveBeenCalledWith('/oauth-model-alias', {
      channel: 'codex',
      aliases: [
        {
          name: 'gpt-5-codex',
          alias: 'team-codex',
          'force-mapping': true,
        },
      ],
    });
  });

  it('drops identity mappings and duplicate aliases before patch', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok' });

    await authFilesApi.saveOauthModelAlias('claude', [
      { name: 'claude-sonnet-4-5', alias: 'claude-sonnet-4-5' },
      { name: 'claude-sonnet-4-5-20250929', alias: 'cs4.5', fork: true },
      { name: 'claude-opus-4-1-20250805', alias: 'CS4.5' },
      { name: 'claude-opus-4-1-20250805', alias: 'opus' },
    ]);

    expect(mocks.patch).toHaveBeenCalledWith('/oauth-model-alias', {
      channel: 'claude',
      aliases: [
        { name: 'claude-sonnet-4-5-20250929', alias: 'cs4.5', fork: true },
        { name: 'claude-opus-4-1-20250805', alias: 'opus' },
      ],
    });
  });
});

describe('authFilesApi list normalization', () => {
  it('preserves same-name auth file rows when authIndex differs', async () => {
    mocks.get.mockResolvedValue({
      files: [
        {
          name: 'sub2api-codex-accounts.codex.json',
          type: 'codex',
          authIndex: 1,
          account: 'second@example.com',
        },
        {
          name: 'sub2api-codex-accounts.codex.json',
          type: 'codex',
          authIndex: 0,
          account: 'first@example.com',
        },
      ],
    });

    const result = await authFilesApi.list();

    expect(mocks.get).toHaveBeenCalledWith('/auth-files');
    expect(result.files).toEqual([
      expect.objectContaining({
        name: 'sub2api-codex-accounts.codex.json',
        authIndex: 0,
        account: 'first@example.com',
      }),
      expect.objectContaining({
        name: 'sub2api-codex-accounts.codex.json',
        authIndex: 1,
        account: 'second@example.com',
      }),
    ]);
    expect(result.total).toBe(2);
  });

  it('still merges duplicate same-name rows when authIndex is absent', async () => {
    mocks.get.mockResolvedValue({
      files: [
        {
          name: 'single-codex.json',
          type: 'codex',
          source: 'runtime',
          status: 'ok',
        },
        {
          name: 'single-codex.json',
          type: 'codex',
          source: 'file',
          path: '/auth/single-codex.json',
          size: 123,
        },
      ],
    });

    const result = await authFilesApi.list();

    expect(result.files).toHaveLength(1);
    expect(result.files[0]).toEqual(
      expect.objectContaining({
        name: 'single-codex.json',
        source: 'file',
        path: '/auth/single-codex.json',
        size: 123,
        status: 'ok',
      })
    );
    expect(result.total).toBe(1);
  });
});

describe('authFilesApi delete contracts', () => {
  it('sends a stable runtime selector while normalizing the deleted physical file name', async () => {
    mocks.delete.mockResolvedValue({ status: 'ok' });

    await expect(authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json')).resolves.toEqual({
      status: 'ok',
      deleted: 1,
      files: ['shared.json'],
      failed: [],
    });

    expect(mocks.delete).toHaveBeenCalledWith('/auth-files?name=runtime-auth-1', {
      headers: {
        'X-CPAMP-Auth-File-Physical-Name': 'shared.json',
      },
    });
  });

  it('maps selector-based failures back to the physical file name', async () => {
    mocks.delete.mockResolvedValue({
      status: 'error',
      deleted: 0,
      failed: [{ name: 'runtime-auth-1', error: 'not found' }],
    });

    await expect(authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json')).resolves.toEqual({
      status: 'error',
      deleted: 0,
      files: [],
      failed: [{ name: 'shared.json', error: 'not found' }],
    });
  });

  it('sends verified delete identities as encoded CPAMP metadata', async () => {
    mocks.delete.mockResolvedValue({ status: 'ok' });
    const identities = [
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
        accountSnapshot: 'user@example.com',
      },
    ];

    await authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json', undefined, identities);

    expect(mocks.delete).toHaveBeenCalledWith('/auth-files?name=runtime-auth-1', {
      headers: {
        'X-CPAMP-Auth-File-Physical-Name': 'shared.json',
        'X-CPAMP-Auth-File-Delete-Identities': encodeURIComponent(JSON.stringify(identities)),
      },
    });
  });

  it('retries the physical source only for CPA plugin-virtual conflicts', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.delete.mockRejectedValueOnce(conflict).mockResolvedValueOnce({ status: 'ok' });
    const verifyFallback = vi.fn().mockResolvedValue(undefined);
    const identities = [
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'user@example.com',
      },
    ];

    await expect(
      authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json', verifyFallback, identities)
    ).resolves.toEqual({ status: 'ok', deleted: 1, files: ['shared.json'], failed: [] });

    expect(verifyFallback).toHaveBeenCalledTimes(1);
    expect(mocks.delete).toHaveBeenCalledTimes(2);
    expect(mocks.delete).toHaveBeenNthCalledWith(1, '/auth-files?name=runtime-auth-1', {
      headers: {
        'X-CPAMP-Auth-File-Physical-Name': 'shared.json',
        'X-CPAMP-Auth-File-Delete-Identities': encodeURIComponent(JSON.stringify(identities)),
      },
    });
    expect(mocks.delete).toHaveBeenNthCalledWith(2, '/auth-files?name=shared.json', {
      headers: {
        'X-CPAMP-Auth-File-Physical-Name': 'shared.json',
        'X-CPAMP-Auth-File-Delete-Identities': encodeURIComponent(JSON.stringify(identities)),
      },
    });
  });

  it('does not widen an exact plugin conflict without post-conflict verification', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.delete.mockRejectedValueOnce(conflict);

    await expect(authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json')).rejects.toBe(
      conflict
    );

    expect(mocks.delete).toHaveBeenCalledTimes(1);
  });

  it('does not widen deletion for unrelated conflicts', async () => {
    const conflict = Object.assign(new Error('credential is busy'), { status: 409 });
    mocks.delete.mockRejectedValueOnce(conflict);

    await expect(authFilesApi.deleteFileByName('runtime-auth-1', 'shared.json')).rejects.toBe(
      conflict
    );

    expect(mocks.delete).toHaveBeenCalledTimes(1);
  });
});

describe('authFilesApi save auth file upload contracts', () => {
  const getUploadedFile = () => {
    const formData = mocks.postForm.mock.calls[0]?.[1];
    expect(formData).toBeInstanceOf(FormData);
    const file = (formData as FormData).get('file');
    expect(file).toBeInstanceOf(File);
    return file as File;
  };

  it('saveText resolves when upload reports one uploaded file', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 1,
      files: ['direct-auth.json'],
      failed: [],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveText('direct-auth.json', '{"type":"codex","access_token":"token"}')
    ).resolves.toBeUndefined();
    expect(mocks.postForm).toHaveBeenCalledWith('/auth-files', expect.any(FormData));
    const file = getUploadedFile();
    expect(file.name).toBe('direct-auth.json');
    await expect(file.text()).resolves.toBe('{"type":"codex","access_token":"token"}');
  });

  it('saveJsonObject resolves when upload succeeds', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 1,
      files: ['converted-auth.json'],
      failed: [],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('converted-auth.json', {
        type: 'codex',
        access_token: 'token',
      })
    ).resolves.toBeUndefined();
    expect(mocks.postForm).toHaveBeenCalledWith('/auth-files', expect.any(FormData));
    const file = getUploadedFile();
    expect(file.name).toBe('converted-auth.json');
    await expect(file.text()).resolves.toBe('{"type":"codex","access_token":"token"}');
  });

  it('saveJsonObject serializes auth file arrays without wrapping them', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 1,
      files: ['converted-auth-array.json'],
      failed: [],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('converted-auth-array.json', [
        {
          type: 'codex',
          access_token: 'first-token',
        },
        {
          type: 'codex',
          access_token: 'second-token',
        },
      ])
    ).resolves.toBeUndefined();
    expect(mocks.postForm).toHaveBeenCalledWith('/auth-files', expect.any(FormData));
    const file = getUploadedFile();
    expect(file.name).toBe('converted-auth-array.json');
    await expect(file.text()).resolves.toBe(
      '[{"type":"codex","access_token":"first-token"},{"type":"codex","access_token":"second-token"}]'
    );
  });

  it('uploadFiles sends multi-file selections as separate requests', async () => {
    // Arrange
    mocks.postForm
      .mockResolvedValueOnce({
        status: 'ok',
        uploaded: 1,
        files: ['first-auth.json'],
        failed: [],
      })
      .mockResolvedValueOnce({
        status: 'ok',
        uploaded: 1,
        files: ['second-auth.json'],
        failed: [],
      });

    const firstFile = new File(['{"type":"codex"}'], 'first-auth.json', {
      type: 'application/json',
    });
    const secondFile = new File(['{"type":"claude"}'], 'second-auth.json', {
      type: 'application/json',
    });

    // Act
    const result = await authFilesApi.uploadFiles([firstFile, secondFile]);

    // Assert
    expect(result).toEqual({
      status: 'ok',
      uploaded: 2,
      files: ['first-auth.json', 'second-auth.json'],
      failed: [],
    });
    expect(mocks.postForm).toHaveBeenCalledTimes(2);

    const firstFormData = mocks.postForm.mock.calls[0]?.[1] as FormData;
    const secondFormData = mocks.postForm.mock.calls[1]?.[1] as FormData;
    expect(Array.from(firstFormData.getAll('file'))).toHaveLength(1);
    expect(Array.from(secondFormData.getAll('file'))).toHaveLength(1);
    expect((firstFormData.get('file') as File).name).toBe('first-auth.json');
    expect((secondFormData.get('file') as File).name).toBe('second-auth.json');
  });

  it('uploadFiles aggregates per-file upload failures after successful uploads', async () => {
    // Arrange
    mocks.postForm
      .mockResolvedValueOnce({
        status: 'ok',
        uploaded: 1,
        files: ['first-auth.json'],
        failed: [],
      })
      .mockRejectedValueOnce(new Error('request body too large'));

    const firstFile = new File(['{"type":"codex"}'], 'first-auth.json', {
      type: 'application/json',
    });
    const secondFile = new File(['{"type":"claude"}'], 'second-auth.json', {
      type: 'application/json',
    });

    // Act
    const result = await authFilesApi.uploadFiles([firstFile, secondFile]);

    // Assert
    expect(result).toEqual({
      status: 'partial',
      uploaded: 1,
      files: ['first-auth.json'],
      failed: [{ name: 'second-auth.json', error: 'request body too large' }],
    });
    expect(mocks.postForm).toHaveBeenCalledTimes(2);
  });

  it('saveJsonObject throws Upload failed when backend reports zero uploaded files without explicit failures', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 0,
      files: [],
      failed: [],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('failed-converted-auth.json', {
        type: 'codex',
        access_token: 'token',
      })
    ).rejects.toThrow('Upload failed');
  });

  it('saveText throws Upload failed when backend reports zero uploaded files without explicit failures', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 0,
      files: [],
      failed: [],
    });

    // Act / Assert
    await expect(authFilesApi.saveText('failed-auth.json', '{"type":"codex"}')).rejects.toThrow(
      'Upload failed'
    );
  });

  it('saveJsonObject surfaces backend failure error text', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'partial',
      uploaded: 0,
      files: [],
      failed: [{ name: 'converted-auth.json', error: 'Storage quota exceeded' }],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('converted-auth.json', {
        type: 'codex',
        access_token: 'token',
      })
    ).rejects.toThrow('Storage quota exceeded');
  });

  it('saveJsonObject throws when backend reports partial failure despite uploaded files', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'partial',
      uploaded: 1,
      files: ['converted-auth.json'],
      failed: [{ name: 'secondary-auth.json', error: 'Invalid auth payload' }],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('converted-auth.json', {
        type: 'codex',
        access_token: 'token',
      })
    ).rejects.toThrow('Invalid auth payload');
  });

  it('saveJsonObject throws when backend reports explicit error status without upload counters', async () => {
    // Arrange
    mocks.postForm.mockResolvedValue({
      status: 'error',
      files: [],
      failed: [],
    });

    // Act / Assert
    await expect(
      authFilesApi.saveJsonObject('failed-status-auth.json', {
        type: 'codex',
        access_token: 'token',
      })
    ).rejects.toThrow('Upload failed');
  });
});

describe('authFilesApi requestCredentialRefresh', () => {
  it('backdates refresh fields for the exact runtime auth selector', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok' });
    const target = {
      name: 'codex-account.json',
      runtimeId: 'codex-runtime-auth-id',
      authIndex: 'auth-1',
      provider: 'codex',
      accountId: 'account-1',
    };

    await authFilesApi.requestCredentialRefresh(target);

    expect(mocks.patch).toHaveBeenCalledWith(
      '/auth-files/fields',
      {
        name: 'codex-runtime-auth-id',
        expired: '2000-01-01T00:00:00Z',
        last_refresh: '2000-01-01T00:00:00Z',
      },
      {
        headers: {
          'X-CPAMP-Auth-File-Mutation-Identity': encodeURIComponent(JSON.stringify([target])),
        },
      }
    );
  });

  it('sends the same verified identity contract for ordinary field patches', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok' });
    const target = {
      name: 'codex-account.json',
      runtimeId: 'codex-runtime-auth-id',
      authIndex: 'auth-1',
      provider: 'codex',
      accountSnapshot: 'user@example.com',
    };

    await authFilesApi.patchFields(target, { priority: 10 });

    expect(mocks.patch).toHaveBeenCalledWith(
      '/auth-files/fields',
      { name: 'codex-runtime-auth-id', priority: 10 },
      {
        headers: {
          'X-CPAMP-Auth-File-Mutation-Identity': encodeURIComponent(JSON.stringify([target])),
        },
      }
    );
  });
});

describe('authFilesApi status targeting', () => {
  it('sends auth_index for an exact status target, including numeric zero', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok', disabled: true });

    await authFilesApi.setStatus(
      { name: 'shared.json', runtimeId: 'runtime-auth-0', authIndex: 0 },
      true
    );

    expect(mocks.patch).toHaveBeenCalledWith('/auth-files/status', {
      name: 'runtime-auth-0',
      auth_index: '0',
      disabled: true,
    });
  });

  it('sends CPAMP identity preconditions for manager-side status revalidation', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok', disabled: true });

    await authFilesApi.setStatus(
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
        accountSnapshot: 'user@example.com',
      },
      true
    );

    expect(mocks.patch).toHaveBeenCalledWith('/auth-files/status', {
      name: 'runtime-auth-1',
      auth_index: 'auth-1',
      disabled: true,
      cpamp_physical_name: 'shared.json',
      cpamp_runtime_id: 'runtime-auth-1',
      cpamp_provider: 'codex',
      cpamp_account_id: 'account-1',
      cpamp_account_snapshot: 'user@example.com',
    });
  });

  it('does not fall back to a physical filename when the runtime target no longer exists', async () => {
    const missing = Object.assign(new Error('auth file not found'), {
      status: 404,
      details: { error: 'auth file not found' },
    });
    mocks.patch.mockRejectedValueOnce(missing);

    await expect(
      authFilesApi.setStatusWithFallback(
        { name: 'shared.json', runtimeId: 'runtime-auth-2', authIndex: 'auth-2' },
        false
      )
    ).rejects.toBe(missing);

    expect(mocks.patch).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledWith('/auth-files/status', {
      name: 'runtime-auth-2',
      auth_index: 'auth-2',
      disabled: false,
    });
  });

  it('does not retry a rejected or failed canonical status mutation', async () => {
    const conflict = Object.assign(new Error('ambiguous'), { status: 409 });
    mocks.patch.mockRejectedValueOnce(conflict);

    await expect(
      authFilesApi.setStatusWithFallback(
        { name: 'shared.json', runtimeId: 'runtime-auth-2', authIndex: 'auth-2' },
        true
      )
    ).rejects.toBe(conflict);
    expect(mocks.patch).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledWith('/auth-files/status', {
      name: 'runtime-auth-2',
      auth_index: 'auth-2',
      disabled: true,
    });
  });

  it('exposes the verified plugin source fallback through the compatibility status helper', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.patch
      .mockRejectedValueOnce(conflict)
      .mockResolvedValueOnce({ status: 'ok', disabled: true });
    const verifyFallback = vi.fn().mockResolvedValue([
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'first@example.com',
      },
    ]);

    await expect(
      authFilesApi.setStatusWithFallback(
        {
          name: 'shared.json',
          runtimeId: 'runtime-auth-1',
          authIndex: 'auth-1',
          provider: 'gemini-cli',
          accountSnapshot: 'first@example.com',
        },
        true,
        verifyFallback
      )
    ).resolves.toEqual({ status: 'ok', disabled: true, mutationScope: 'source-file' });

    expect(verifyFallback).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledTimes(2);
  });

  it('sends the complete member identity snapshot for an explicit source-file status change', async () => {
    mocks.patch.mockResolvedValue({ status: 'ok', disabled: true });

    await expect(
      authFilesApi.setVerifiedSourceFileStatus(
        {
          name: 'shared.json',
          runtimeId: 'shared.json',
          authIndex: 'auth-source',
          provider: 'gemini-cli',
          accountId: 'account-source',
        },
        true,
        [
          {
            name: 'shared.json',
            runtimeId: 'shared.json',
            authIndex: 'auth-source',
            provider: 'gemini-cli',
            accountId: 'account-source',
          },
          {
            name: 'shared.json',
            runtimeId: 'runtime-child',
            authIndex: 'auth-child',
            provider: 'gemini-cli',
            accountId: 'account-child',
          },
        ]
      )
    ).resolves.toEqual({ status: 'ok', disabled: true, mutationScope: 'source-file' });

    expect(mocks.patch).toHaveBeenCalledWith('/auth-files/status', {
      name: 'shared.json',
      auth_index: 'auth-source',
      disabled: true,
      cpamp_source_file: true,
      cpamp_source_identities: [
        {
          name: 'shared.json',
          runtime_id: 'shared.json',
          auth_index: 'auth-source',
          provider: 'gemini-cli',
          account_id: 'account-source',
        },
        {
          name: 'shared.json',
          runtime_id: 'runtime-child',
          auth_index: 'auth-child',
          provider: 'gemini-cli',
          account_id: 'account-child',
        },
      ],
      cpamp_physical_name: 'shared.json',
      cpamp_runtime_id: 'shared.json',
      cpamp_provider: 'gemini-cli',
      cpamp_account_id: 'account-source',
    });
  });

  it('retries the physical source only for CPA plugin-virtual conflicts', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.patch
      .mockRejectedValueOnce(conflict)
      .mockResolvedValueOnce({ status: 'ok', disabled: true });
    const verifyFallback = vi.fn().mockResolvedValue([
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountId: 'account-1',
      },
      {
        name: 'shared.json',
        runtimeId: 'runtime-auth-2',
        authIndex: 'auth-2',
        provider: 'gemini-cli',
        accountId: 'account-2',
      },
    ]);

    await expect(
      authFilesApi.setStatusWithPluginSourceFallback(
        { name: 'shared.json', runtimeId: 'runtime-auth-2', authIndex: 'auth-2' },
        true,
        verifyFallback
      )
    ).resolves.toEqual({ status: 'ok', disabled: true, mutationScope: 'source-file' });

    expect(verifyFallback).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledTimes(2);
    expect(mocks.patch).toHaveBeenNthCalledWith(1, '/auth-files/status', {
      name: 'runtime-auth-2',
      auth_index: 'auth-2',
      disabled: true,
    });
    expect(mocks.patch).toHaveBeenNthCalledWith(2, '/auth-files/status', {
      name: 'shared.json',
      auth_index: 'auth-2',
      disabled: true,
      cpamp_source_file: true,
      cpamp_source_identities: [
        {
          name: 'shared.json',
          runtime_id: 'runtime-auth-1',
          auth_index: 'auth-1',
          provider: 'gemini-cli',
          account_id: 'account-1',
        },
        {
          name: 'shared.json',
          runtime_id: 'runtime-auth-2',
          auth_index: 'auth-2',
          provider: 'gemini-cli',
          account_id: 'account-2',
        },
      ],
    });
  });

  it('does not retry the physical source without a post-conflict verifier', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.patch.mockRejectedValueOnce(conflict);

    await expect(
      authFilesApi.setStatusWithPluginSourceFallback(
        { name: 'shared.json', runtimeId: 'runtime-auth-2', authIndex: 'auth-2' },
        true
      )
    ).rejects.toBe(conflict);

    expect(mocks.patch).toHaveBeenCalledTimes(1);
  });

  it('does not retry the physical source when post-conflict verification fails', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    const verificationError = new Error('status target changed');
    const verifyFallback = vi.fn().mockRejectedValue(verificationError);
    mocks.patch.mockRejectedValueOnce(conflict);

    await expect(
      authFilesApi.setStatusWithPluginSourceFallback(
        { name: 'shared.json', runtimeId: 'runtime-auth-2', authIndex: 'auth-2' },
        true,
        verifyFallback
      )
    ).rejects.toBe(verificationError);

    expect(verifyFallback).toHaveBeenCalledTimes(1);
    expect(mocks.patch).toHaveBeenCalledTimes(1);
  });

  it('does not retry a plugin conflict when the canonical runtime is already the source name', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.patch.mockRejectedValueOnce(conflict);

    await expect(
      authFilesApi.setStatusWithPluginSourceFallback(
        { name: 'shared.json', runtimeId: 'shared.json', authIndex: 'auth-source' },
        true
      )
    ).rejects.toBe(conflict);

    expect(mocks.patch).toHaveBeenCalledTimes(1);
  });
});

describe('authFilesApi patchFieldsForAuthIndexes', () => {
  const getUploadedFile = () => {
    const formData = mocks.postForm.mock.calls[0]?.[1];
    expect(formData).toBeInstanceOf(FormData);
    const file = (formData as FormData).get('file');
    expect(file).toBeInstanceOf(File);
    return file as File;
  };

  it('updates only matching auth records in an auth array', async () => {
    const rawText = JSON.stringify([
      { type: 'codex', authIndex: 0, priority: 1, websocket: true },
      { type: 'codex', auth_index: 'auth-2', priority: 2 },
      { type: 'codex', authIndex: 'auth-3', priority: 3, websocket: true },
    ]);
    mocks.getRaw.mockResolvedValue({
      data: new Blob([rawText]),
    });
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 1,
      files: ['shared-codex.json'],
      failed: [],
    });

    const targets = [
      {
        name: 'shared-codex.json',
        runtimeId: 'runtime-1',
        authIndex: 0,
        provider: 'codex',
      },
      {
        name: 'shared-codex.json',
        runtimeId: 'runtime-2',
        authIndex: 'auth-2',
        provider: 'codex',
      },
    ];
    const sourceIdentities = [
      ...targets,
      {
        name: 'shared-codex.json',
        runtimeId: 'runtime-3',
        authIndex: 'auth-3',
        provider: 'codex',
      },
    ];

    await authFilesApi.patchFieldsForAuthIndexes('shared-codex.json', targets, sourceIdentities, {
      priority: 10,
      websockets: false,
    });

    expect(mocks.getRaw).toHaveBeenCalledWith('/auth-files/download?name=shared-codex.json', {
      responseType: 'blob',
    });
    expect(mocks.postForm).toHaveBeenCalledWith('/auth-files', expect.any(FormData), {
      headers: {
        'X-CPAMP-Auth-File-Write-Identities': encodeURIComponent(
          JSON.stringify([
            { ...sourceIdentities[0], authIndex: '0' },
            sourceIdentities[1],
            sourceIdentities[2],
          ])
        ),
        'X-CPAMP-Auth-File-Write-Content-SHA256': sha256RawTextHex(rawText),
      },
    });
    const file = getUploadedFile();
    expect(file.name).toBe('shared-codex.json');
    await expect(file.text()).resolves.toBe(
      JSON.stringify([
        { type: 'codex', authIndex: 0, priority: 10, websockets: false },
        { type: 'codex', auth_index: 'auth-2', priority: 10, websockets: false },
        { type: 'codex', authIndex: 'auth-3', priority: 3, websocket: true },
      ])
    );
  });

  it('falls back to a verified physical source write for plugin virtual fields', async () => {
    const conflict = Object.assign(
      new Error(
        'plugin virtual auth cannot be modified directly; edit or delete the source auth file'
      ),
      { status: 409 }
    );
    mocks.patch.mockRejectedValueOnce(conflict);
    const rawText = JSON.stringify({
      type: 'gemini-cli',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: 'old',
      using_api: false,
    });
    mocks.getRaw.mockResolvedValue({ data: new Blob([rawText]) });
    mocks.postForm.mockResolvedValue({
      status: 'ok',
      uploaded: 1,
      files: ['plugin-source.json'],
      failed: [],
    });
    const target = {
      name: 'plugin-source.json',
      runtimeId: 'runtime-auth-1',
      authIndex: 'auth-1',
      provider: 'gemini-cli',
      accountId: 'account-1',
    };

    await authFilesApi.patchFieldsWithPluginSourceFallback(
      target,
      { note: 'updated', using_api: true },
      [target]
    );

    expect(mocks.patch).toHaveBeenCalledTimes(1);
    expect(mocks.getRaw).toHaveBeenCalledWith('/auth-files/download?name=plugin-source.json', {
      responseType: 'blob',
    });
    expect(mocks.postForm).toHaveBeenCalledWith('/auth-files', expect.any(FormData), {
      headers: {
        'X-CPAMP-Auth-File-Write-Identities': encodeURIComponent(JSON.stringify([target])),
        'X-CPAMP-Auth-File-Write-Content-SHA256': sha256RawTextHex(rawText),
      },
    });
    await expect(getUploadedFile().text()).resolves.toBe(
      JSON.stringify({
        type: 'gemini-cli',
        auth_index: 'auth-1',
        account_id: 'account-1',
        note: 'updated',
        using_api: true,
      })
    );
  });

  it('rejects a multi-member plugin source stored as one root object', async () => {
    mocks.getRaw.mockResolvedValue({
      data: new Blob([
        JSON.stringify({
          type: 'gemini-cli',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'old',
        }),
      ]),
    });
    const target = {
      name: 'plugin-source.json',
      runtimeId: 'runtime-auth-1',
      authIndex: 'auth-1',
      provider: 'gemini-cli',
      accountId: 'account-1',
    };

    await expect(
      authFilesApi.patchFieldsForAuthIndexes(
        'plugin-source.json',
        [target],
        [
          target,
          {
            name: 'plugin-source.json',
            runtimeId: 'runtime-auth-2',
            authIndex: 'auth-2',
            provider: 'gemini-cli',
            accountId: 'account-2',
          },
        ],
        { note: 'updated' }
      )
    ).rejects.toThrow('Auth file patch target changed');

    expect(mocks.postForm).not.toHaveBeenCalled();
  });

  it('rejects an auth-index replacement before reuploading the source file', async () => {
    mocks.getRaw.mockResolvedValue({
      data: new Blob([
        JSON.stringify([
          {
            type: 'codex',
            auth_index: 'auth-1',
            account_id: 'replacement-account',
          },
        ]),
      ]),
    });

    await expect(
      authFilesApi.patchFieldsForAuthIndexes(
        'shared-codex.json',
        [
          {
            name: 'shared-codex.json',
            runtimeId: 'runtime-1',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'original-account',
          },
        ],
        [
          {
            name: 'shared-codex.json',
            runtimeId: 'runtime-1',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'original-account',
          },
        ],
        { priority: 10 }
      )
    ).rejects.toThrow('Auth file patch target changed');

    expect(mocks.postForm).not.toHaveBeenCalled();
  });
});
