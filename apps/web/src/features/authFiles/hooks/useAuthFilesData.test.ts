import { act, createElement } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';

const { mocks } = vi.hoisted(() => {
  return {
    mocks: {
      list: vi.fn(),
      saveJsonObject: vi.fn(),
      uploadFiles: vi.fn(),
      deleteFiles: vi.fn(),
      deleteFile: vi.fn(),
      patchFields: vi.fn(),
      patchFieldsForAuthIndexes: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
    },
  };
});

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (options && typeof options.name === 'string') {
        return `${key}:${options.name}`;
      }
      if (
        options &&
        typeof options.uploaded === 'number' &&
        typeof options.total === 'number' &&
        typeof options.names === 'string'
      ) {
        return `${key}:${options.uploaded}/${options.total}:${options.names}`;
      }
      return key;
    },
  }),
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api', () => ({
  authFilesApi: {
    list: mocks.list,
    saveJsonObject: mocks.saveJsonObject,
    uploadFiles: mocks.uploadFiles,
    deleteFiles: mocks.deleteFiles,
    deleteFile: mocks.deleteFile,
    patchFields: mocks.patchFields,
    patchFieldsForAuthIndexes: mocks.patchFieldsForAuthIndexes,
  },
}));

import {
  buildPastedAuthJsonPayloads,
  prepareAuthFilesForUpload,
  useAuthFilesData,
} from './useAuthFilesData';
import {
  getCodexInspectionOwnedDisableFileNames,
  recordCodexInspectionDisableOwnership,
} from '@/features/monitoring/model/codexInspectionOwnership';

type UseAuthFilesDataHarness = {
  getCurrent: () => ReturnType<typeof useAuthFilesData>;
  getSavingHistory: () => boolean[];
  unmount: () => void;
};

const createStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      values.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      values.delete(key);
    }),
    clear: vi.fn(() => values.clear()),
  } as unknown as Storage;
};

const mountUseAuthFilesData = (connectionFingerprint?: string): UseAuthFilesDataHarness => {
  let hook: ReturnType<typeof useAuthFilesData> | null = null;
  let lastSavingState: boolean | undefined;
  const savingHistory: boolean[] = [];
  let renderer: ReactTestRenderer | null = null;

  const captureHook = (value: ReturnType<typeof useAuthFilesData>) => {
    hook = value;
    if (value.authJsonPasteSaving !== lastSavingState) {
      lastSavingState = value.authJsonPasteSaving;
      savingHistory.push(value.authJsonPasteSaving);
    }
  };

  function HookHarness() {
    captureHook(useAuthFilesData({ connectionFingerprint }));
    return null;
  }

  act(() => {
    renderer = create(createElement(HookHarness));
  });

  return {
    getCurrent: () => {
      if (!hook) {
        throw new Error('Failed to mount useAuthFilesData test harness');
      }
      return hook;
    },
    getSavingHistory: () => [...savingHistory],
    unmount: () => {
      if (!renderer) return;
      act(() => {
        renderer?.unmount();
      });
    },
  };
};

beforeEach(() => {
  mocks.list.mockReset();
  mocks.saveJsonObject.mockReset();
  mocks.uploadFiles.mockReset();
  mocks.deleteFiles.mockReset();
  mocks.deleteFile.mockReset();
  mocks.patchFields.mockReset();
  mocks.patchFieldsForAuthIndexes.mockReset();
  mocks.showNotification.mockReset();
  mocks.showConfirmation.mockReset();

  mocks.list.mockResolvedValue({ files: [] });
  mocks.saveJsonObject.mockResolvedValue(undefined);
  mocks.uploadFiles.mockResolvedValue({ status: 'ok', uploaded: 0, files: [], failed: [] });
  mocks.deleteFiles.mockResolvedValue({ deleted: 0, failed: [], files: [] });
  mocks.deleteFile.mockResolvedValue({ deleted: 0, failed: [], files: [] });
  mocks.patchFields.mockResolvedValue(undefined);
  mocks.patchFieldsForAuthIndexes.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('buildPastedAuthJsonPayloads', () => {
  it('keeps explicit file names for pasted CPA auth JSON', () => {
    const input = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    };

    const result = buildPastedAuthJsonPayloads('cpa', 'custom-auth.json', JSON.stringify(input));

    expect(result).toEqual([{ fileName: 'custom-auth.json', authJson: input }]);
  });

  it('keeps explicit file names for pasted session auth JSON when a custom name is provided', () => {
    const result = buildPastedAuthJsonPayloads(
      'session',
      'my-work-account.json',
      JSON.stringify({
        user: { email: 'Session.User+tag@example.com' },
        account: { id: 'session-account' },
        accessToken: 'plain-access-token',
      })
    );

    expect(result[0].fileName).toBe('my-work-account.json');
  });

  it('derives a default codex file name for pasted session auth JSON', () => {
    const result = buildPastedAuthJsonPayloads(
      'session',
      'codex-account.json',
      JSON.stringify({
        user: { email: 'Session.User+tag@example.com' },
        account: { id: 'session-account' },
        accessToken: 'plain-access-token',
      })
    );

    expect(result[0].fileName).toBe('codex-session-session.user+tag@example.com.json');
    expect(result[0].authJson).toMatchObject({
      type: 'codex',
      email: 'Session.User+tag@example.com',
      account_id: 'session-account',
      access_token: 'plain-access-token',
    });
  });

  it('derives separate default file names for multi-account sub2api auth JSON', () => {
    const result = buildPastedAuthJsonPayloads(
      'sub2api',
      'codex-account.json',
      JSON.stringify({
        exported_at: '2026-06-01T12:00:00.000Z',
        proxies: [],
        accounts: [
          {
            name: 'First OpenAI',
            platform: 'openai',
            type: 'oauth',
            credentials: {
              access_token: 'first-access-token',
              email: 'first@example.com',
            },
          },
          {
            name: 'Second OpenAI',
            platform: 'openai',
            type: 'oauth',
            credentials: {
              access_token: 'second-access-token',
              email: 'second@example.com',
            },
          },
        ],
      })
    );

    expect(result).toHaveLength(2);
    expect(result[0]).toEqual({
      fileName: expect.stringMatching(/^codex-[a-f0-9]{8}-first@example\.com\.json$/),
      authJson: expect.objectContaining({
        type: 'codex',
        email: 'first@example.com',
        access_token: 'first-access-token',
      }),
    });
    expect(result[1]).toEqual({
      fileName: expect.stringMatching(/^codex-[a-f0-9]{8}-second@example\.com\.json$/),
      authJson: expect.objectContaining({
        type: 'codex',
        email: 'second@example.com',
        access_token: 'second-access-token',
      }),
    });
  });
});

describe('prepareAuthFilesForUpload', () => {
  it('preserves ordinary CPA auth JSON files without rewriting them', async () => {
    const file = new File(
      [JSON.stringify({ type: 'codex', email: 'user@example.com', access_token: 'token' })],
      'existing-auth.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result).toEqual({
      files: [file],
      failures: [],
      convertedSourceCount: 0,
    });
    expect(result.files[0]).toBe(file);
  });

  it('preserves valid CPA auth JSON with export-like metadata without rewriting it', async () => {
    const file = new File(
      [
        JSON.stringify({
          type: 'custom-provider',
          token: 'provider-secret',
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
        }),
      ],
      'custom-provider-auth.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result).toEqual({
      files: [file],
      failures: [],
      convertedSourceCount: 0,
    });
    expect(result.files[0]).toBe(file);
  });

  it('converts an uploaded sub2api export into separate CPA auth files', async () => {
    const file = new File(
      [
        JSON.stringify({
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
          accounts: [
            {
              name: 'First OpenAI',
              platform: 'openai',
              type: 'oauth',
              credentials: {
                access_token: 'first-access-token',
                email: 'first@example.com',
              },
            },
            {
              name: 'Second OpenAI',
              platform: 'openai',
              type: 'oauth',
              credentials: {
                access_token: 'second-access-token',
                email: 'second@example.com',
              },
            },
          ],
        }),
      ],
      'sub2api-export.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result.failures).toEqual([]);
    expect(result.convertedSourceCount).toBe(1);
    expect(result.files).toHaveLength(2);
    expect(result.files.map((item) => item.name)).toEqual([
      expect.stringMatching(/^codex-[a-f0-9]{8}-first@example\.com\.json$/),
      expect.stringMatching(/^codex-[a-f0-9]{8}-second@example\.com\.json$/),
    ]);
    for (const convertedFile of result.files) {
      const parsed = JSON.parse(await convertedFile.text()) as unknown;
      expect(parsed).toBeTypeOf('object');
      expect(Array.isArray(parsed)).toBe(false);
    }
  });

  it('reports an invalid detected sub2api export without uploading the source file', async () => {
    const file = new File(
      [
        JSON.stringify({
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
          accounts: [
            {
              name: 'Missing Token',
              platform: 'openai',
              type: 'oauth',
              credentials: { email: 'missing@example.com' },
            },
          ],
        }),
      ],
      'invalid-sub2api-export.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result.files).toEqual([]);
    expect(result.convertedSourceCount).toBe(0);
    expect(result.failures).toEqual([
      {
        name: 'invalid-sub2api-export.json',
        error: expect.stringContaining('missing credentials.access_token'),
      },
    ]);
  });

  it('rejects an empty sub2api export instead of uploading it as an ordinary auth file', async () => {
    const file = new File(
      [JSON.stringify({ exported_at: '2026-06-01T12:00:00.000Z', proxies: [], accounts: [] })],
      'empty-sub2api-export.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result.files).toEqual([]);
    expect(result.failures).toEqual([
      {
        name: 'empty-sub2api-export.json',
        error: expect.stringContaining('No sub2api OpenAI OAuth account'),
      },
    ]);
  });

  it('rejects malformed sub2api account entries instead of uploading the export unchanged', async () => {
    const file = new File(
      [
        JSON.stringify({
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
          accounts: [{ name: 'Malformed', platform: 'openai', type: 'oauth', credentials: null }],
        }),
      ],
      'malformed-sub2api-export.json',
      { type: 'application/json' }
    );

    const result = await prepareAuthFilesForUpload([file]);

    expect(result.files).toEqual([]);
    expect(result.failures).toEqual([
      {
        name: 'malformed-sub2api-export.json',
        error: expect.stringContaining('missing credentials'),
      },
    ]);
  });

  it.each([
    { label: 'null', accounts: null },
    { label: 'object', accounts: {} },
    { label: 'string', accounts: 'invalid' },
  ])(
    'rejects a sub2api export whose accounts value is $label instead of uploading it unchanged',
    async ({ label, accounts }) => {
      const file = new File(
        [
          JSON.stringify({
            exported_at: '2026-06-01T12:00:00.000Z',
            proxies: [],
            accounts,
          }),
        ],
        `malformed-accounts-${label}.json`,
        { type: 'application/json' }
      );

      const result = await prepareAuthFilesForUpload([file]);

      expect(result.files).toEqual([]);
      expect(result.failures).toEqual([
        {
          name: `malformed-accounts-${label}.json`,
          error: expect.stringContaining('accounts must be an array'),
        },
      ]);
    }
  );
});

describe('useAuthFilesData handleFileChange', () => {
  it('auto-converts an uploaded sub2api export before calling the backend upload API', async () => {
    const hook = mountUseAuthFilesData();
    const file = new File(
      [
        JSON.stringify({
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
          accounts: [
            {
              name: 'First OpenAI',
              platform: 'openai',
              type: 'oauth',
              credentials: {
                access_token: 'first-access-token',
                email: 'first@example.com',
              },
            },
            {
              name: 'Second OpenAI',
              platform: 'openai',
              type: 'oauth',
              credentials: {
                access_token: 'second-access-token',
                email: 'second@example.com',
              },
            },
          ],
        }),
      ],
      'sub2api-export.json',
      { type: 'application/json' }
    );
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => ({
      status: 'ok',
      uploaded: files.length,
      files: files.map((item) => item.name),
      failed: [],
    }));
    const target = {
      files: [file] as unknown as FileList,
      value: 'sub2api-export.json',
    };

    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });

    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    const uploadedFiles = mocks.uploadFiles.mock.calls[0]?.[0] as File[];
    expect(uploadedFiles).toHaveLength(2);
    expect(uploadedFiles.every((item) => item.name !== file.name)).toBe(true);
    expect(target.value).toBe('');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.upload_success (2/2)',
      'success'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('does not report direct upload success when the backend returns an explicit failure status', async () => {
    const hook = mountUseAuthFilesData();
    const file = new File(
      [
        JSON.stringify({
          exported_at: '2026-06-01T12:00:00.000Z',
          proxies: [],
          accounts: [
            {
              name: 'First OpenAI',
              platform: 'openai',
              type: 'oauth',
              credentials: {
                access_token: 'first-access-token',
                email: 'first@example.com',
              },
            },
          ],
        }),
      ],
      'sub2api-export.json',
      { type: 'application/json' }
    );
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => ({
      status: 'error',
      uploaded: files.length,
      files: files.map((item) => item.name),
      failed: [],
    }));
    const target = {
      files: [file] as unknown as FileList,
      value: 'sub2api-export.json',
    };

    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).not.toHaveBeenCalledWith('auth_files.upload_success', 'success');
    expect(mocks.showNotification).toHaveBeenCalledWith('notification.upload_failed', 'error');
    hook.unmount();
  });
});

describe('useAuthFilesData savePastedAuthJson', () => {
  it('saves converted session JSON with derived default file name and reloads files', async () => {
    const hook = mountUseAuthFilesData();
    const sessionInput = JSON.stringify({
      user: { email: 'Session.User+tag@example.com' },
      account: { id: 'session-account' },
      accessToken: 'plain-access-token',
    });

    const savedName = await hook
      .getCurrent()
      .savePastedAuthJson('session', 'codex-account.json', sessionInput);

    expect(savedName).toEqual(['codex-session-session.user+tag@example.com.json']);
    expect(mocks.saveJsonObject).toHaveBeenCalledWith(
      'codex-session-session.user+tag@example.com.json',
      expect.objectContaining({
        type: 'codex',
        email: 'Session.User+tag@example.com',
        account_id: 'session-account',
        access_token: 'plain-access-token',
      })
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.paste_success:codex-session-session.user+tag@example.com.json',
      'success'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('saves CPA JSON unchanged with explicit file name', async () => {
    const hook = mountUseAuthFilesData();
    const cpaInput = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    };

    const savedName = await hook
      .getCurrent()
      .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(cpaInput));

    expect(savedName).toEqual(['custom-auth.json']);
    expect(mocks.saveJsonObject).toHaveBeenCalledWith('custom-auth.json', cpaInput);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('saves converted sub2api JSON as separate CPA auth files', async () => {
    const hook = mountUseAuthFilesData();
    const sub2apiInput = JSON.stringify({
      exported_at: '2026-06-01T12:00:00.000Z',
      proxies: [],
      accounts: [
        {
          name: 'First OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'first-access-token',
            email: 'first@example.com',
          },
        },
        {
          name: 'Second OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'second-access-token',
            email: 'second@example.com',
          },
        },
      ],
    });
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => ({
      status: 'ok',
      uploaded: files.length,
      files: files.map((file) => file.name),
      failed: [],
    }));

    const savedNames = await hook
      .getCurrent()
      .savePastedAuthJson('sub2api', 'codex-account.json', sub2apiInput);

    expect(savedNames).toEqual([
      expect.stringMatching(/^codex-[a-f0-9]{8}-first@example\.com\.json$/),
      expect.stringMatching(/^codex-[a-f0-9]{8}-second@example\.com\.json$/),
    ]);
    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    const uploadedFiles = mocks.uploadFiles.mock.calls[0]?.[0] as File[];
    expect(uploadedFiles).toHaveLength(2);
    const uploadedJson = await Promise.all(
      uploadedFiles.map(async (file) => JSON.parse(await file.text()) as Record<string, unknown>)
    );
    expect(uploadedJson).toEqual([
      expect.objectContaining({
        type: 'codex',
        email: 'first@example.com',
        access_token: 'first-access-token',
      }),
      expect.objectContaining({
        type: 'codex',
        email: 'second@example.com',
        access_token: 'second-access-token',
      }),
    ]);
    expect(uploadedJson.every((item) => !Array.isArray(item))).toBe(true);
    expect(mocks.showNotification).toHaveBeenCalledWith('auth_files.paste_success_many', 'success');
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('rejects an explicit partial upload status even when all generated files are counted', async () => {
    const hook = mountUseAuthFilesData();
    const sub2apiInput = JSON.stringify({
      exported_at: '2026-06-01T12:00:00.000Z',
      proxies: [],
      accounts: [
        {
          name: 'First OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'first-access-token',
            email: 'first@example.com',
          },
        },
        {
          name: 'Second OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'second-access-token',
            email: 'second@example.com',
          },
        },
      ],
    });
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => ({
      status: 'partial',
      uploaded: files.length,
      files: files.map((file) => file.name),
      failed: [],
    }));

    await expect(
      hook.getCurrent().savePastedAuthJson('sub2api', 'codex-account.json', sub2apiInput)
    ).rejects.toThrow('notification.save_failed');

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.paste_success_many',
      'success'
    );
    hook.unmount();
  });

  it('reloads files and reports the failed name after a partial sub2api paste upload', async () => {
    const hook = mountUseAuthFilesData();
    const sub2apiInput = JSON.stringify({
      exported_at: '2026-06-01T12:00:00.000Z',
      proxies: [],
      accounts: [
        {
          name: 'First OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'first-access-token',
            email: 'first@example.com',
          },
        },
        {
          name: 'Second OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'second-access-token',
            email: 'second@example.com',
          },
        },
      ],
    });
    let failedName = '';
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => {
      failedName = files[1].name;
      return {
        status: 'partial',
        uploaded: 1,
        files: [files[0].name],
        failed: [{ name: failedName, error: 'upload failed' }],
      };
    });

    await expect(
      hook.getCurrent().savePastedAuthJson('sub2api', 'codex-account.json', sub2apiInput)
    ).rejects.toThrow(`auth_files.paste_error_partial:1/2:${failedName}`);

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.paste_success_many',
      'success'
    );
    hook.unmount();
  });

  it('keeps the partial upload error and warns when its file reload also fails', async () => {
    const hook = mountUseAuthFilesData();
    const sub2apiInput = JSON.stringify({
      exported_at: '2026-06-01T12:00:00.000Z',
      proxies: [],
      accounts: [
        {
          name: 'First OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'first-access-token',
            email: 'first@example.com',
          },
        },
        {
          name: 'Second OpenAI',
          platform: 'openai',
          type: 'oauth',
          credentials: {
            access_token: 'second-access-token',
            email: 'second@example.com',
          },
        },
      ],
    });
    let failedName = '';
    mocks.uploadFiles.mockImplementationOnce(async (files: File[]) => {
      failedName = files[1].name;
      return {
        status: 'partial',
        uploaded: 1,
        files: [files[0].name],
        failed: [{ name: failedName, error: 'upload failed' }],
      };
    });
    mocks.list.mockRejectedValueOnce(new Error('reload failed'));

    await expect(
      hook.getCurrent().savePastedAuthJson('sub2api', 'codex-account.json', sub2apiInput)
    ).rejects.toThrow(`auth_files.paste_error_partial:1/2:${failedName}`);

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.refresh_failed: reload failed',
      'warning'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.paste_success_many',
      'success'
    );
    hook.unmount();
  });

  it('waits for file reload completion before resolving pasted save success', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    let resolveList: (() => void) | undefined;
    mocks.list.mockImplementationOnce(
      () =>
        new Promise<{ files: [] }>((resolve) => {
          resolveList = () => resolve({ files: [] });
        })
    );

    const settled = vi.fn();
    const savePromise = hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput);
    void savePromise.then(settled);

    await Promise.resolve();
    await Promise.resolve();

    expect(settled).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();

    expect(resolveList).toBeTypeOf('function');
    resolveList?.();
    await savePromise;
    expect(settled).toHaveBeenCalledWith(['custom-auth.json']);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.paste_success:custom-auth.json',
      'success'
    );
    hook.unmount();
  });

  it('sets authJsonPasteSaving true during save and resets false after success', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    let resolveUpload: (() => void) | undefined;
    mocks.saveJsonObject.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveUpload = resolve;
        })
    );

    const savePromise = hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput);
    await act(async () => {
      await Promise.resolve();
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(true);

    expect(resolveUpload).toBeTypeOf('function');
    resolveUpload?.();
    await expect(savePromise).resolves.toEqual(['custom-auth.json']);
    await act(async () => {
      await Promise.resolve();
    });

    expect(hook.getCurrent().authJsonPasteSaving).toBe(false);
    const savingHistory = hook.getSavingHistory();
    expect(savingHistory).toContain(true);
    expect(savingHistory[savingHistory.length - 1]).toBe(false);
    hook.unmount();
  });

  it('rejects a concurrent pasted save before starting a duplicate upload', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    let resolveUpload: (() => void) | undefined;
    mocks.saveJsonObject.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveUpload = resolve;
        })
    );

    const firstSave = hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput);
    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput)
    ).rejects.toThrow('auth_files.paste_error_save_in_progress');

    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
    expect(resolveUpload).toBeTypeOf('function');
    resolveUpload?.();
    await expect(firstSave).resolves.toEqual(['custom-auth.json']);
    hook.unmount();
  });

  it('throws on invalid conversion and does not upload or show success notification', async () => {
    const hook = mountUseAuthFilesData();
    const invalidInput = JSON.stringify({ foo: 'bar' });

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', invalidInput)
    ).rejects.toThrow();

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();
    expect(mocks.list).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('throws a generic save failure on upload failure and does not show success notification or reload files', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    mocks.saveJsonObject.mockRejectedValueOnce(
      new Error('upload failed for token sk-secret-value')
    );

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput)
    ).rejects.toThrow('notification.save_failed');

    expect(mocks.showNotification).not.toHaveBeenCalled();
    expect(mocks.list).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('resolves saved file name when reload fails after upload and shows refresh warning', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    mocks.list.mockClear();
    mocks.list.mockRejectedValueOnce(new Error('reload failed'));

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput)
    ).resolves.toEqual(['custom-auth.json']);

    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.paste_success:custom-auth.json',
      'success'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.refresh_failed: reload failed',
      'warning'
    );
    hook.unmount();
  });

  it('sets authJsonPasteSaving true during save and resets false after failure', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    let rejectUpload: ((reason?: unknown) => void) | undefined;
    mocks.saveJsonObject.mockImplementationOnce(
      () =>
        new Promise<void>((_, reject) => {
          rejectUpload = reject;
        })
    );

    const savePromise = hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput);
    await act(async () => {
      await Promise.resolve();
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(true);

    expect(rejectUpload).toBeTypeOf('function');
    rejectUpload?.(new Error('upload failed'));
    await expect(savePromise).rejects.toThrow('notification.save_failed');
    await act(async () => {
      await Promise.resolve();
    });

    expect(hook.getCurrent().authJsonPasteSaving).toBe(false);
    const savingHistory = hook.getSavingHistory();
    expect(savingHistory).toContain(true);
    expect(savingHistory[savingHistory.length - 1]).toBe(false);
    hook.unmount();
  });

  it('allows retrying pasted save after an upload failure', async () => {
    const hook = mountUseAuthFilesData();
    const validInput = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    mocks.saveJsonObject.mockRejectedValueOnce(new Error('upload failed'));

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput)
    ).rejects.toThrow('notification.save_failed');
    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', validInput)
    ).resolves.toEqual(['custom-auth.json']);

    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(2);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.paste_success:custom-auth.json',
      'success'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData handleDelete', () => {
  const disabledFile = {
    name: 'owned.json',
    type: 'codex',
    auth_index: 'auth-1',
    disabled: true,
  } as AuthFileItem;

  it('keeps ownership when CPA reports a logical delete failure', async () => {
    vi.stubGlobal('localStorage', createStorage());
    recordCodexInspectionDisableOwnership('scope-a', {
      fileName: 'owned.json',
      authIndex: 'auth-1',
      accountId: null,
    });
    mocks.deleteFile.mockResolvedValueOnce({
      deleted: 0,
      files: [],
      failed: [{ name: 'owned.json', error: 'still in use' }],
    });
    const hook = mountUseAuthFilesData('scope-a');

    act(() => hook.getCurrent().handleDelete('owned.json'));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(Array.from(getCodexInspectionOwnedDisableFileNames('scope-a', [disabledFile]))).toEqual([
      'owned.json',
    ]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.delete_failed: still in use',
      'error'
    );
    hook.unmount();
  });

  it('clears ownership only for the active connection after a successful delete', async () => {
    vi.stubGlobal('localStorage', createStorage());
    for (const scope of ['scope-a', 'scope-b']) {
      recordCodexInspectionDisableOwnership(scope, {
        fileName: 'owned.json',
        authIndex: 'auth-1',
        accountId: null,
      });
    }
    mocks.deleteFile.mockResolvedValueOnce({
      deleted: 1,
      files: ['owned.json'],
      failed: [],
    });
    const hook = mountUseAuthFilesData('scope-a');

    act(() => hook.getCurrent().handleDelete('owned.json'));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(getCodexInspectionOwnedDisableFileNames('scope-a', [disabledFile]).size).toBe(0);
    expect(Array.from(getCodexInspectionOwnedDisableFileNames('scope-b', [disabledFile]))).toEqual([
      'owned.json',
    ]);
    hook.unmount();
  });
});

describe('useAuthFilesData handleDeleteAll', () => {
  it('deletes only the provided filtered files for custom result filters', async () => {
    const hook = mountUseAuthFilesData();
    const resetResultFilters = vi.fn();
    const resetFilterToAll = vi.fn();

    mocks.list.mockResolvedValueOnce({
      files: [
        { name: 'codex-limited.json', type: 'codex' },
        { name: 'codex-ok.json', type: 'codex' },
      ],
    });
    mocks.deleteFiles.mockResolvedValueOnce({
      deleted: 1,
      failed: [],
      files: ['codex-limited.json'],
    });

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    act(() => {
      hook.getCurrent().handleDeleteAll({
        filter: 'all',
        problemOnly: false,
        disabledOnly: false,
        healthyOnly: false,
        filteredFiles: [{ name: 'codex-limited.json', type: 'codex' }],
        onResetFilterToAll: resetFilterToAll,
        onResetProblemOnly: vi.fn(),
        onResetDisabledOnly: vi.fn(),
        onResetHealthyOnly: vi.fn(),
        onResetResultFilters: resetResultFilters,
      });
    });

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    expect(confirmation?.onConfirm).toBeTypeOf('function');
    expect(mocks.showConfirmation).toHaveBeenCalledWith(
      expect.objectContaining({
        message: 'auth_files.delete_filtered_result_confirm_file_scope',
      })
    );

    await act(async () => {
      await confirmation?.onConfirm?.();
    });

    expect(mocks.deleteFiles).toHaveBeenCalledWith(['codex-limited.json']);
    expect(resetFilterToAll).not.toHaveBeenCalled();
    expect(resetResultFilters).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.delete_filtered_result_success',
      'success'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData batchPatchFields', () => {
  it('patches selected auth indexes from the same file in one request', async () => {
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [
          { name: 'shared-codex.json', authIndex: 'auth-1' },
          { name: 'shared-codex.json', authIndex: 'auth-2' },
          { name: 'shared-codex.json', authIndex: 'auth-1' },
        ],
        { priority: 10 }
      );
    });

    expect(mocks.patchFieldsForAuthIndexes).toHaveBeenCalledWith(
      'shared-codex.json',
      ['auth-1', 'auth-2'],
      { priority: 10 }
    );
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 2, failed: 0, failedNames: [] });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_fields_success',
      'success'
    );
    hook.unmount();
  });

  it('falls back to file-level field patching when auth index is absent', async () => {
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook
        .getCurrent()
        .batchPatchFields([{ name: 'single-codex.json' }], { websockets: false });
    });

    expect(mocks.patchFields).toHaveBeenCalledWith('single-codex.json', { websockets: false });
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    hook.unmount();
  });
});
