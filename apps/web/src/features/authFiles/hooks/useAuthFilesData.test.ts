import { act, createElement, useLayoutEffect } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { AuthFileLookupTarget } from '@/services/api';
import {
  readAuthFileStatusAuthIndex,
  readAuthFileStatusPhysicalName,
  readAuthFileStatusRuntimeId,
} from '@/utils/authFileStatusMutation';

const { mocks } = vi.hoisted(() => {
  return {
    mocks: {
      list: vi.fn(),
      lookup: vi.fn(),
      saveJsonObject: vi.fn(),
      uploadFiles: vi.fn(),
      deleteFiles: vi.fn(),
      deleteFile: vi.fn(),
      deleteFileByName: vi.fn(),
      patchFields: vi.fn(),
      patchFieldsWithPluginSourceFallback: vi.fn(),
      patchFieldsForAuthIndexes: vi.fn(),
      setStatus: vi.fn(),
      requestCredentialRefresh: vi.fn(),
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
      if (
        options &&
        typeof options.success === 'number' &&
        typeof options.failed === 'number' &&
        typeof options.review === 'number'
      ) {
        return `${key}:${options.success}/${options.failed}/${options.review}`;
      }
      if (
        key === 'auth_files.batch_status_success' &&
        options &&
        typeof options.count === 'number'
      ) {
        return `${key}:${options.count}`;
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
    lookup: mocks.lookup,
    saveJsonObject: mocks.saveJsonObject,
    uploadFiles: mocks.uploadFiles,
    deleteFiles: mocks.deleteFiles,
    deleteFile: mocks.deleteFile,
    deleteFileByName: mocks.deleteFileByName,
    patchFields: mocks.patchFields,
    patchFieldsWithPluginSourceFallback: mocks.patchFieldsWithPluginSourceFallback,
    patchFieldsForAuthIndexes: mocks.patchFieldsForAuthIndexes,
    setStatus: mocks.setStatus,
    setStatusWithFallback: mocks.setStatus,
    setStatusWithPluginSourceFallback: mocks.setStatus,
    setVerifiedSourceFileStatus: mocks.setStatus,
    requestCredentialRefresh: mocks.requestCredentialRefresh,
  },
}));

import {
  buildPastedAuthJsonPayloads,
  prepareAuthFilesForUpload,
  useAuthFilesData,
  type AuthFilesCredentialMutation,
} from './useAuthFilesData';
import {
  getCodexInspectionOwnedDisableIdentityKeys,
  getCodexInspectionOwnershipIdentityKey,
  recordCodexInspectionDisableOwnership,
} from '@/features/monitoring/model/codexInspectionOwnership';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';

type UseAuthFilesDataHarness = {
  getCurrent: () => ReturnType<typeof useAuthFilesData>;
  getSavingHistory: () => boolean[];
  rerender: (connectionFingerprint?: string) => void;
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

const createDeferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, resolve, reject };
};

const mockAuthFileLookup = (files: AuthFileItem[]) => {
  mocks.lookup.mockImplementation(async ({ name, authIndex }: AuthFileLookupTarget) =>
    files.filter(
      (file) =>
        (readAuthFileStatusPhysicalName(file) === name ||
          readAuthFileStatusRuntimeId(file) === name) &&
        (authIndex === undefined || readAuthFileStatusAuthIndex(file) === String(authIndex))
    )
  );
};

const mountUseAuthFilesData = (
  connectionFingerprint?: string,
  onConnectionLayout?: (
    value: ReturnType<typeof useAuthFilesData>,
    connectionFingerprint?: string
  ) => void,
  onCredentialFilesChanged?: (mutation: AuthFilesCredentialMutation) => void,
  requestScope?: { apiBase: string; managementKey: string }
): UseAuthFilesDataHarness => {
  let currentConnectionFingerprint = connectionFingerprint;
  let lastLayoutConnectionFingerprint: string | undefined | symbol = Symbol('uninitialized');
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
    const value = useAuthFilesData({
      connectionFingerprint: currentConnectionFingerprint,
      requestScope,
      onCredentialMutation: onCredentialFilesChanged,
    });
    captureHook(value);
    useLayoutEffect(() => {
      if (lastLayoutConnectionFingerprint === currentConnectionFingerprint) return;
      lastLayoutConnectionFingerprint = currentConnectionFingerprint;
      onConnectionLayout?.(value, currentConnectionFingerprint);
    });
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
    rerender: (nextConnectionFingerprint?: string) => {
      if (!renderer) return;
      currentConnectionFingerprint = nextConnectionFingerprint;
      act(() => {
        renderer?.update(createElement(HookHarness));
      });
    },
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
  mocks.lookup.mockReset();
  mocks.saveJsonObject.mockReset();
  mocks.uploadFiles.mockReset();
  mocks.deleteFiles.mockReset();
  mocks.deleteFile.mockReset();
  mocks.deleteFileByName.mockReset();
  mocks.patchFields.mockReset();
  mocks.patchFieldsWithPluginSourceFallback.mockReset();
  mocks.patchFieldsForAuthIndexes.mockReset();
  mocks.setStatus.mockReset();
  mocks.requestCredentialRefresh.mockReset();
  mocks.showNotification.mockReset();
  mocks.showConfirmation.mockReset();

  mocks.list.mockResolvedValue({ files: [] });
  mocks.lookup.mockResolvedValue([]);
  mocks.saveJsonObject.mockResolvedValue(undefined);
  mocks.uploadFiles.mockResolvedValue({ status: 'ok', uploaded: 0, files: [], failed: [] });
  mocks.deleteFiles.mockResolvedValue({ deleted: 0, failed: [], files: [] });
  mocks.deleteFile.mockResolvedValue({ deleted: 0, failed: [], files: [] });
  mocks.deleteFileByName.mockResolvedValue({ deleted: 0, failed: [], files: [] });
  mocks.patchFields.mockResolvedValue(undefined);
  mocks.patchFieldsWithPluginSourceFallback.mockResolvedValue(undefined);
  mocks.patchFieldsForAuthIndexes.mockResolvedValue(undefined);
  mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: false });
  mocks.requestCredentialRefresh.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('useAuthFilesData revalidation', () => {
  it('keeps successful data and selection through pending, successful and failed refreshes', async () => {
    const hook = mountUseAuthFilesData('a');
    expect(hook.getCurrent().initialLoading).toBe(true);
    const first = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(first.promise);
    let pending!: Promise<unknown>;
    act(() => {
      pending = hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().initialLoading).toBe(true);
    await act(async () => {
      first.resolve({ files: [{ name: 'old.json' }] });
      await pending;
    });
    const selectionKey = getAuthFileSelectionKey({ name: 'old.json' });
    act(() => hook.getCurrent().toggleSelect(selectionKey));
    const next = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(next.promise);
    act(() => {
      pending = hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().loading).toBe(true);
    expect(hook.getCurrent().initialLoading).toBe(false);
    expect(hook.getCurrent().files[0].name).toBe('old.json');
    await act(async () => {
      next.resolve({ files: [{ name: 'old.json' }, { name: 'new.json' }] });
      await pending;
    });
    expect(hook.getCurrent().files).toHaveLength(2);
    mocks.list.mockRejectedValueOnce(new Error('offline'));
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().files).toHaveLength(2);
    expect(hook.getCurrent().error).toBe('offline');
    expect(hook.getCurrent().selectedFiles.has(selectionKey)).toBe(true);
    hook.unmount();
  });

  it('keeps a successful empty result and rejects old connection responses', async () => {
    const hook = mountUseAuthFilesData('a');
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    const old = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(old.promise);
    let pending!: Promise<unknown>;
    act(() => {
      pending = hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().initialLoading).toBe(false);
    hook.rerender('b');
    expect(hook.getCurrent().initialLoading).toBe(true);
    await act(async () => {
      old.resolve({ files: [{ name: 'wrong-scope.json' }] });
      await pending;
    });
    expect(hook.getCurrent().files).toEqual([]);
    expect(hook.getCurrent().initialLoading).toBe(true);
    hook.unmount();
  });

  it('discards earlier refreshes that complete after the latest response', async () => {
    const hook = mountUseAuthFilesData('a');
    const old = createDeferred<{ files: AuthFileItem[] }>();
    const fresh = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    let first!: Promise<unknown>, second!: Promise<unknown>;
    act(() => {
      first = hook.getCurrent().loadFiles();
      second = hook.getCurrent().loadFiles();
    });
    await act(async () => {
      fresh.resolve({ files: [{ name: 'latest.json' }] });
      await second;
    });
    await act(async () => {
      old.resolve({ files: [{ name: 'stale.json' }] });
      await first;
    });
    expect(hook.getCurrent().files[0].name).toBe('latest.json');
    hook.unmount();
  });
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
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: uploadedFiles.map((item) => item.name),
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('does not report direct upload success when the backend returns an explicit failure status', async () => {
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    mocks.uploadFiles.mockImplementationOnce(async () => ({
      status: 'error',
      uploaded: 0,
      files: [],
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

    expect(mocks.list).not.toHaveBeenCalled();
    expect(onCredentialFilesChanged).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalledWith('auth_files.upload_success', 'success');
    expect(mocks.showNotification).toHaveBeenCalledWith('notification.upload_failed', 'error');
    hook.unmount();
  });

  it('does not let an old connection upload clear or notify the new upload', async () => {
    const oldUpload = createDeferred<{
      status: string;
      uploaded: number;
      files: string[];
      failed: Array<{ name: string; error: string }>;
    }>();
    const newUpload = createDeferred<{
      status: string;
      uploaded: number;
      files: string[];
      failed: Array<{ name: string; error: string }>;
    }>();
    const onCredentialMutation = vi.fn();
    mocks.uploadFiles.mockReturnValueOnce(oldUpload.promise).mockReturnValueOnce(newUpload.promise);
    const hook = mountUseAuthFilesData('connection-a', undefined, onCredentialMutation);
    const makeTarget = (name: string) => ({
      files: [new File(['{}'], name, { type: 'application/json' })] as unknown as FileList,
      value: name,
    });
    const oldTarget = makeTarget('old.json');
    const newTarget = makeTarget('new.json');
    let oldPromise!: Promise<void>;
    let newPromise!: Promise<void>;

    await act(async () => {
      oldPromise = hook
        .getCurrent()
        .handleFileChange({ target: oldTarget } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
      await Promise.resolve();
    });
    expect(hook.getCurrent().uploading).toBe(true);

    hook.rerender('connection-b');
    expect(hook.getCurrent().uploading).toBe(false);
    await act(async () => {
      newPromise = hook
        .getCurrent()
        .handleFileChange({ target: newTarget } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
      await Promise.resolve();
    });
    expect(hook.getCurrent().uploading).toBe(true);

    await act(async () => {
      oldUpload.resolve({ status: 'ok', uploaded: 1, files: ['old.json'], failed: [] });
      await oldPromise;
    });
    expect(hook.getCurrent().uploading).toBe(true);
    expect(onCredentialMutation).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();

    await act(async () => {
      newUpload.resolve({ status: 'ok', uploaded: 1, files: ['new.json'], failed: [] });
      await newPromise;
    });
    expect(hook.getCurrent().uploading).toBe(false);
    expect(onCredentialMutation).toHaveBeenCalledTimes(1);
    expect(onCredentialMutation).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: ['new.json'],
    });
    expect(mocks.showNotification).toHaveBeenCalledWith('auth_files.upload_success', 'success');
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
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: ['custom-auth.json'],
    });
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
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    const uploadedFiles = mocks.uploadFiles.mock.calls[0]?.[0] as File[];
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: uploadedFiles.map((file) => file.name),
    });
    hook.unmount();
  });

  it('reloads files and reports the failed name after a partial sub2api paste upload', async () => {
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    const uploadedFiles = mocks.uploadFiles.mock.calls[0]?.[0] as File[];
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: [uploadedFiles[0].name],
    });
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
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
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
    expect(onCredentialFilesChanged).not.toHaveBeenCalled();
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

  it('does not let an old connection pasted save clear or notify the new save', async () => {
    const oldSave = createDeferred<void>();
    const newSave = createDeferred<void>();
    const onCredentialMutation = vi.fn();
    mocks.saveJsonObject.mockReturnValueOnce(oldSave.promise).mockReturnValueOnce(newSave.promise);
    const hook = mountUseAuthFilesData('connection-a', undefined, onCredentialMutation);
    const input = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: 'existing-access-token',
    });
    let oldPromise!: Promise<string[]>;
    let newPromise!: Promise<string[]>;

    await act(async () => {
      oldPromise = hook.getCurrent().savePastedAuthJson('cpa', 'old.json', input);
      await Promise.resolve();
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(true);

    hook.rerender('connection-b');
    expect(hook.getCurrent().authJsonPasteSaving).toBe(false);
    await act(async () => {
      newPromise = hook.getCurrent().savePastedAuthJson('cpa', 'new.json', input);
      await Promise.resolve();
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(true);

    await act(async () => {
      oldSave.resolve();
      await oldPromise;
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(true);
    expect(onCredentialMutation).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();

    await act(async () => {
      newSave.resolve();
      await newPromise;
    });
    expect(hook.getCurrent().authJsonPasteSaving).toBe(false);
    expect(onCredentialMutation).toHaveBeenCalledTimes(1);
    expect(onCredentialMutation).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: ['new.json'],
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.paste_success:new.json',
      'success'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData handleDelete', () => {
  const disabledFile = {
    id: 'runtime-owned',
    name: 'owned.json',
    type: 'codex',
    auth_index: 'auth-1',
    account: 'owned@example.com',
    disabled: true,
  } as AuthFileItem;

  it('keeps ownership when CPA reports a logical delete failure', async () => {
    vi.stubGlobal('localStorage', createStorage());
    recordCodexInspectionDisableOwnership('scope-a', {
      fileName: 'owned.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountId: null,
      accountSnapshot: 'owned@example.com',
    });
    mocks.list.mockResolvedValueOnce({ files: [disabledFile] });
    mocks.deleteFileByName.mockResolvedValueOnce({
      deleted: 0,
      files: [],
      failed: [{ name: 'owned.json', error: 'still in use' }],
    });
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData('scope-a', undefined, onCredentialFilesChanged);

    act(() => hook.getCurrent().handleDelete(disabledFile));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | {
          onConfirm?: () => Promise<void>;
          secondConfirmation?: { message?: string; confirmText?: string };
        }
      | undefined;
    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(confirmation?.secondConfirmation).toMatchObject({
      message: 'auth_files.delete_second_confirm:owned.json',
      confirmText: 'auth_files.delete_second_action',
    });
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).toHaveBeenCalledWith(
      'runtime-owned',
      'owned.json',
      expect.any(Function),
      [
        {
          name: 'owned.json',
          runtimeId: 'runtime-owned',
          authIndex: 'auth-1',
          provider: 'codex',
          accountSnapshot: 'owned@example.com',
        },
      ]
    );

    expect(
      Array.from(getCodexInspectionOwnedDisableIdentityKeys('scope-a', [disabledFile]))
    ).toEqual([
      getCodexInspectionOwnershipIdentityKey({
        fileName: 'owned.json',
        provider: 'codex',
        authIndex: 'auth-1',
        accountId: null,
        accountSnapshot: 'owned@example.com',
      }),
    ]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.delete_failed: still in use',
      'error'
    );
    expect(onCredentialFilesChanged).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('clears ownership only for the active connection after a successful delete', async () => {
    vi.stubGlobal('localStorage', createStorage());
    for (const scope of ['scope-a', 'scope-b']) {
      recordCodexInspectionDisableOwnership(scope, {
        fileName: 'owned.json',
        provider: 'codex',
        authIndex: 'auth-1',
        accountId: null,
        accountSnapshot: 'owned@example.com',
      });
    }
    mocks.list.mockResolvedValueOnce({ files: [disabledFile] });
    mocks.deleteFileByName.mockResolvedValueOnce({
      deleted: 1,
      files: ['owned.json'],
      failed: [],
    });
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData('scope-a', undefined, onCredentialFilesChanged);

    act(() => hook.getCurrent().handleDelete(disabledFile));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void>; secondConfirmation?: { message?: string } }
      | undefined;
    expect(confirmation?.secondConfirmation?.message).toBe(
      'auth_files.delete_second_confirm:owned.json'
    );
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).toHaveBeenCalledWith(
      'runtime-owned',
      'owned.json',
      expect.any(Function),
      [
        {
          name: 'owned.json',
          runtimeId: 'runtime-owned',
          authIndex: 'auth-1',
          provider: 'codex',
          accountSnapshot: 'owned@example.com',
        },
      ]
    );

    expect(getCodexInspectionOwnedDisableIdentityKeys('scope-a', [disabledFile]).size).toBe(0);
    expect(
      Array.from(getCodexInspectionOwnedDisableIdentityKeys('scope-b', [disabledFile]))
    ).toEqual([
      getCodexInspectionOwnershipIdentityKey({
        fileName: 'owned.json',
        provider: 'codex',
        authIndex: 'auth-1',
        accountId: null,
        accountSnapshot: 'owned@example.com',
      }),
    ]);
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'source-files-changed',
      fileNames: ['owned.json'],
    });
    hook.unmount();
  });

  it('does not execute a delete confirmation after the CPA connection changes', async () => {
    mocks.list.mockResolvedValue({ files: [disabledFile] });
    const requestScope = {
      apiBase: 'https://scope-a.example.test',
      managementKey: 'scope-a-key',
    };
    const hook = mountUseAuthFilesData('scope-a', undefined, undefined, requestScope);
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().handleDelete(disabledFile));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;

    hook.rerender('scope-b');
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(mocks.list).toHaveBeenCalledWith(requestScope);
    hook.unmount();
  });

  it('does not revive a delete confirmation after switching away and back to the same connection', async () => {
    mocks.list.mockResolvedValue({ files: [disabledFile] });
    const hook = mountUseAuthFilesData('scope-a');
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().handleDelete(disabledFile));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;

    hook.rerender('scope-b');
    hook.rerender('scope-a');
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('warns that a shared card delete removes every credential and uses a stable selector', async () => {
    const first = {
      id: 'runtime-shared-1',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'first@example.com',
    } as AuthFileItem;
    const second = {
      id: 'runtime-shared-2',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-2',
      account: 'second@example.com',
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [first, second] });
    mocks.deleteFileByName.mockResolvedValueOnce({
      deleted: 1,
      files: ['shared.json'],
      failed: [],
    });
    const hook = mountUseAuthFilesData();
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().handleDelete(second));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | {
          message?: string;
          onConfirm?: () => Promise<void>;
          secondConfirmation?: { message?: string };
        }
      | undefined;
    expect(confirmation?.message).toBe('auth_files.delete_shared_confirm:shared.json');
    expect(confirmation?.secondConfirmation?.message).toBe(
      'auth_files.delete_shared_second_confirm:shared.json'
    );
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).toHaveBeenCalledWith('shared.json', 'shared.json', undefined, [
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'first@example.com',
      },
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-2',
        authIndex: 'auth-2',
        provider: 'codex',
        accountSnapshot: 'second@example.com',
      },
    ]);
    hook.unmount();
  });

  it('refuses a shared file delete when its physical name collides with another runtime ID', async () => {
    const first = {
      id: 'runtime-shared-1',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'first@example.com',
    } as AuthFileItem;
    const second = {
      id: 'runtime-shared-2',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-2',
      account: 'second@example.com',
    } as AuthFileItem;
    const collision = {
      id: 'shared.json',
      name: 'other.json',
      type: 'codex',
      auth_index: 'other-auth',
      account: 'other@example.com',
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [first, second, collision] });
    const hook = mountUseAuthFilesData();
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().handleDelete(second));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.delete_failed: auth_files.delete_target_changed',
      'error'
    );
    hook.unmount();
  });

  it('refuses deletion when membership changes after confirmation', async () => {
    const original = {
      id: 'runtime-original',
      name: 'replaceable.json',
      type: 'xai',
      auth_index: 'auth-1',
      account: 'original@example.com',
    } as AuthFileItem;
    const replacement = {
      ...original,
      id: 'runtime-replacement',
      account: 'replacement@example.com',
    } as AuthFileItem;
    mocks.list.mockResolvedValueOnce({ files: [replacement] });
    const hook = mountUseAuthFilesData();

    act(() => hook.getCurrent().handleDelete(original));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.delete_failed: auth_files.delete_target_changed',
      'error'
    );
    hook.unmount();
  });

  it('revalidates plugin source membership after the runtime delete conflict', async () => {
    const original = {
      id: 'runtime-plugin',
      name: 'plugin-source.json',
      type: 'gemini-cli',
      auth_index: 'auth-1',
      account: 'original@example.com',
    } as AuthFileItem;
    const addedSibling = {
      id: 'runtime-plugin-2',
      name: 'plugin-source.json',
      type: 'gemini-cli',
      auth_index: 'auth-2',
      account: 'sibling@example.com',
    } as AuthFileItem;
    mocks.list
      .mockResolvedValueOnce({ files: [original] })
      .mockResolvedValueOnce({ files: [original, addedSibling] });
    mocks.deleteFileByName.mockImplementationOnce(
      async (_selector: string, _physicalName: string, verifyFallback?: () => Promise<void>) => {
        await verifyFallback?.();
        return { deleted: 1, files: ['plugin-source.json'], failed: [] };
      }
    );
    const hook = mountUseAuthFilesData();

    act(() => hook.getCurrent().handleDelete(original));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).toHaveBeenCalledWith(
      'runtime-plugin',
      'plugin-source.json',
      expect.any(Function),
      [
        {
          name: 'plugin-source.json',
          runtimeId: 'runtime-plugin',
          authIndex: 'auth-1',
          provider: 'gemini-cli',
          accountSnapshot: 'original@example.com',
        },
      ]
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.delete_failed: auth_files.delete_target_changed',
      'error'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData batchDelete', () => {
  it('requires a second confirmation and uses verified runtime selectors', async () => {
    const first = {
      id: 'runtime-first',
      name: 'first.json',
      type: 'codex',
      auth_index: 'auth-first',
      account: 'first@example.com',
    } as AuthFileItem;
    const second = {
      id: 'runtime-second',
      name: 'second.json',
      type: 'xai',
      auth_index: 'auth-second',
      account: 'second@example.com',
    } as AuthFileItem;
    const hook = mountUseAuthFilesData();
    mocks.list.mockResolvedValue({ files: [first, second] });
    mocks.deleteFileByName
      .mockResolvedValueOnce({ deleted: 1, failed: [], files: ['first.json'] })
      .mockResolvedValueOnce({ deleted: 1, failed: [], files: ['second.json'] });
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().batchDelete([first, second]));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void>; secondConfirmation?: { message?: string } }
      | undefined;

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(confirmation?.secondConfirmation?.message).toBe('auth_files.delete_many_second_confirm');

    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).toHaveBeenNthCalledWith(
      1,
      'runtime-first',
      'first.json',
      expect.any(Function),
      [
        {
          name: 'first.json',
          runtimeId: 'runtime-first',
          authIndex: 'auth-first',
          provider: 'codex',
          accountSnapshot: 'first@example.com',
        },
      ]
    );
    expect(mocks.deleteFileByName).toHaveBeenNthCalledWith(
      2,
      'runtime-second',
      'second.json',
      expect.any(Function),
      [
        {
          name: 'second.json',
          runtimeId: 'runtime-second',
          authIndex: 'auth-second',
          provider: 'xai',
          accountSnapshot: 'second@example.com',
        },
      ]
    );
    expect(mocks.deleteFiles).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses a selected delete when the physical path is replaced after confirmation', async () => {
    const original = {
      id: 'runtime-original',
      name: 'replaceable.json',
      type: 'xai',
      auth_index: 'auth-original',
      account: 'original@example.com',
    } as AuthFileItem;
    const replacement = {
      ...original,
      id: 'runtime-replacement',
      auth_index: 'auth-replacement',
      account: 'replacement@example.com',
    } as AuthFileItem;
    mocks.list
      .mockResolvedValueOnce({ files: [original] })
      .mockResolvedValueOnce({ files: [replacement] });
    const hook = mountUseAuthFilesData();
    await act(async () => hook.getCurrent().loadFiles());

    act(() => hook.getCurrent().batchDelete([original]));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as
      | { onConfirm?: () => Promise<void> }
      | undefined;
    await act(async () => confirmation?.onConfirm?.());

    expect(mocks.deleteFileByName).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.delete_filtered_partial',
      'warning'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData status targeting', () => {
  it('does not let an old connection load overwrite the new connection files', async () => {
    const oldFiles = [
      {
        id: 'runtime-old',
        name: 'old.json',
        type: 'codex',
        auth_index: 'auth-old',
        disabled: false,
      },
    ] as AuthFileItem[];
    const newFiles = [
      {
        id: 'runtime-new',
        name: 'new.json',
        type: 'codex',
        auth_index: 'auth-new',
        disabled: true,
      },
    ] as AuthFileItem[];
    const oldLoad = createDeferred<{ files: AuthFileItem[] }>();
    const newLoad = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(oldLoad.promise).mockReturnValueOnce(newLoad.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let oldPromise!: Promise<AuthFileItem[] | undefined>;
    let newPromise!: Promise<AuthFileItem[] | undefined>;

    act(() => {
      oldPromise = hook.getCurrent().loadFiles();
    });
    hook.rerender('connection-b');
    act(() => {
      newPromise = hook.getCurrent().loadFiles();
    });

    await act(async () => {
      newLoad.resolve({ files: newFiles });
      expect(await newPromise).toEqual(newFiles);
    });
    expect(hook.getCurrent().files).toEqual(newFiles);
    expect(hook.getCurrent().loading).toBe(false);

    await act(async () => {
      oldLoad.resolve({ files: oldFiles });
      expect(await oldPromise).toBeUndefined();
    });
    expect(hook.getCurrent().files).toEqual(newFiles);
    expect(hook.getCurrent().loading).toBe(false);
    hook.unmount();
  });

  it('keeps the newest same-connection load result when requests finish out of order', async () => {
    const firstFiles = [
      {
        id: 'runtime-first',
        name: 'first.json',
        type: 'codex',
        auth_index: 'auth-first',
        disabled: false,
      },
    ] as AuthFileItem[];
    const secondFiles = [
      {
        id: 'runtime-second',
        name: 'second.json',
        type: 'codex',
        auth_index: 'auth-second',
        disabled: true,
      },
    ] as AuthFileItem[];
    const firstLoad = createDeferred<{ files: AuthFileItem[] }>();
    const secondLoad = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(firstLoad.promise).mockReturnValueOnce(secondLoad.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let firstPromise!: Promise<AuthFileItem[] | undefined>;
    let secondPromise!: Promise<AuthFileItem[] | undefined>;

    act(() => {
      firstPromise = hook.getCurrent().loadFiles();
      secondPromise = hook.getCurrent().loadFiles();
    });
    await act(async () => {
      secondLoad.resolve({ files: secondFiles });
      expect(await secondPromise).toEqual(secondFiles);
    });
    await act(async () => {
      firstLoad.resolve({ files: firstFiles });
      expect(await firstPromise).toBeUndefined();
    });

    expect(hook.getCurrent().files).toEqual(secondFiles);
    expect(hook.getCurrent().loading).toBe(false);
    hook.unmount();
  });

  it('rejects a strict same-connection load when a newer request supersedes it', async () => {
    const firstFiles = [
      {
        id: 'runtime-first',
        name: 'first.json',
        type: 'codex',
        auth_index: 'auth-first',
      },
    ] as AuthFileItem[];
    const secondFiles = [
      {
        id: 'runtime-second',
        name: 'second.json',
        type: 'codex',
        auth_index: 'auth-second',
      },
    ] as AuthFileItem[];
    const firstLoad = createDeferred<{ files: AuthFileItem[] }>();
    const secondLoad = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(firstLoad.promise).mockReturnValueOnce(secondLoad.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let strictPromise!: Promise<AuthFileItem[] | undefined>;
    let latestPromise!: Promise<AuthFileItem[] | undefined>;

    act(() => {
      strictPromise = hook.getCurrent().loadFiles({ throwOnError: true });
      latestPromise = hook.getCurrent().loadFiles();
    });
    await act(async () => {
      secondLoad.resolve({ files: secondFiles });
      expect(await latestPromise).toEqual(secondFiles);
    });

    let strictError: unknown;
    await act(async () => {
      firstLoad.resolve({ files: firstFiles });
      try {
        await strictPromise;
      } catch (error) {
        strictError = error;
      }
    });

    expect(strictError).toBeInstanceOf(Error);
    expect(hook.getCurrent().files).toEqual(secondFiles);
    expect(hook.getCurrent().loading).toBe(false);
    hook.unmount();
  });

  it('blocks a duplicate same-key batch while a refresh is pending', async () => {
    const files = [
      {
        id: 'runtime-single',
        name: 'single.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'single@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup(files.map((file) => ({ ...file, disabled: true })));
      return { status: 'ok', disabled: true };
    });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    const pendingLookup = createDeferred<AuthFileItem[]>();
    mocks.lookup.mockReturnValueOnce(pendingLookup.promise);
    let batchPromise!: Promise<void>;
    act(() => {
      batchPromise = hook.getCurrent().batchSetStatus(
        [
          {
            name: 'single.json',
            authIndex: 'auth-1',
            provider: 'codex',
            accountSnapshot: 'single@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(1);

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).not.toHaveBeenCalled();

    await act(async () => {
      pendingLookup.resolve(files);
      await batchPromise;
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'single.json',
        runtimeId: 'runtime-single',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'single@example.com',
      },
      true,
      expect.any(Function)
    );
    hook.unmount();
  });

  it('keeps the original batch pending while a duplicate same-key batch is rejected', async () => {
    const files = [
      {
        id: 'runtime-single',
        name: 'single.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'single@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup(files.map((file) => ({ ...file, disabled: true })));
      return { status: 'ok', disabled: true };
    });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    const pendingLookup = createDeferred<AuthFileItem[]>();
    mocks.lookup.mockReturnValueOnce(pendingLookup.promise);
    let singlePromise!: Promise<void>;
    act(() => {
      singlePromise = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(2);

    await act(async () => {
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'single.json',
            authIndex: 'auth-1',
            provider: 'codex',
            accountSnapshot: 'single@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(2);
    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);

    await act(async () => {
      pendingLookup.resolve(files);
      await singlePromise;
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'single.json',
        runtimeId: 'runtime-single',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'single@example.com',
      },
      true,
      expect.any(Function)
    );
    hook.unmount();
  });

  it('does not let an old connection single-item batch clear or update the new operation', async () => {
    const oldFile = {
      id: 'runtime-shared',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'old@example.com',
      disabled: false,
    } as AuthFileItem;
    const newFile = {
      ...oldFile,
      account: 'new@example.com',
    } as AuthFileItem;
    const oldStatus = createDeferred<{ status: string; disabled: boolean }>();
    const newStatus = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [oldFile] })
      .mockResolvedValueOnce({ files: [newFile] });
    mockAuthFileLookup([oldFile]);
    mocks.setStatus.mockReturnValueOnce(oldStatus.promise).mockReturnValueOnce(newStatus.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let oldPromise!: Promise<void>;
    let newPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      oldPromise = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(oldFile)], false);
      await Promise.resolve();
    });
    expect(mocks.setStatus).toHaveBeenCalledTimes(1);

    hook.rerender('connection-b');
    mockAuthFileLookup([newFile]);
    await act(async () => {
      await hook.getCurrent().loadFiles();
      newPromise = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(newFile)], true);
      await Promise.resolve();
    });
    expect(mocks.setStatus).toHaveBeenCalledTimes(2);

    await act(async () => {
      oldStatus.resolve({ status: 'ok', disabled: true });
      await oldPromise;
    });
    expect(hook.getCurrent().files).toEqual([newFile]);
    expect(mocks.showNotification).not.toHaveBeenCalled();

    await act(async () => {
      newStatus.resolve({ status: 'ok', disabled: false });
      await newPromise;
    });
    expect(hook.getCurrent().files).toEqual([newFile]);
    expect(mocks.showNotification).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('does not let an old connection batch result clear or update the new batch operation', async () => {
    const oldFile = {
      id: 'runtime-shared',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'old@example.com',
      disabled: false,
    } as AuthFileItem;
    const newFile = {
      ...oldFile,
      account: 'new@example.com',
    } as AuthFileItem;
    const oldStatus = createDeferred<{ status: string; disabled: boolean }>();
    const newStatus = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [oldFile] })
      .mockResolvedValueOnce({ files: [newFile] });
    mockAuthFileLookup([oldFile]);
    mocks.setStatus.mockReturnValueOnce(oldStatus.promise).mockReturnValueOnce(newStatus.promise);
    const hook = mountUseAuthFilesData('connection-a');
    const oldTarget = {
      name: oldFile.name,
      runtimeId: oldFile.id,
      authIndex: oldFile.auth_index as string,
      provider: 'codex',
      accountSnapshot: String(oldFile.account),
    };
    const newTarget = { ...oldTarget, accountSnapshot: String(newFile.account) };
    let oldPromise!: Promise<void>;
    let newPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      oldPromise = hook.getCurrent().batchSetStatus([oldTarget], false);
      await Promise.resolve();
    });
    expect(mocks.setStatus).toHaveBeenCalledTimes(1);

    hook.rerender('connection-b');
    mockAuthFileLookup([newFile]);
    await act(async () => {
      await hook.getCurrent().loadFiles();
      newPromise = hook.getCurrent().batchSetStatus([newTarget], true);
      await Promise.resolve();
    });
    expect(mocks.setStatus).toHaveBeenCalledTimes(2);
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);

    await act(async () => {
      oldStatus.resolve({ status: 'ok', disabled: true });
      await oldPromise;
    });
    expect(hook.getCurrent().files).toEqual([newFile]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);
    expect(mocks.showNotification).not.toHaveBeenCalled();

    await act(async () => {
      newStatus.resolve({ status: 'ok', disabled: false });
      await newPromise;
    });
    expect(hook.getCurrent().files).toEqual([newFile]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    expect(mocks.showNotification).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('does not roll a failed single-item batch over a newer refresh state', async () => {
    const initialFile = {
      id: 'runtime-single',
      name: 'single.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'single@example.com',
      disabled: false,
    } as AuthFileItem;
    const refreshedFile = { ...initialFile, disabled: true } as AuthFileItem;
    const status = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [refreshedFile] });
    mockAuthFileLookup([initialFile]);
    mocks.setStatus.mockReturnValueOnce(status.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let mutationPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook
        .getCurrent()
        .batchSetStatus([getAuthFilePatchTarget(initialFile)], false);
      await Promise.resolve();
    });
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);

    await act(async () => {
      mockAuthFileLookup([refreshedFile]);
      status.reject(new Error('status failed'));
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'single.json' });
    hook.unmount();
  });

  it('does not roll failed batch entries over a newer refresh state', async () => {
    const initialFile = {
      id: 'runtime-single',
      name: 'single.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'single@example.com',
      disabled: false,
    } as AuthFileItem;
    const refreshedFile = { ...initialFile, disabled: true } as AuthFileItem;
    const status = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [refreshedFile] });
    mockAuthFileLookup([initialFile]);
    mocks.setStatus.mockReturnValueOnce(status.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let mutationPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook.getCurrent().batchSetStatus(
        [
          {
            name: initialFile.name,
            runtimeId: initialFile.id,
            authIndex: initialFile.auth_index as string,
            provider: 'codex',
            accountSnapshot: String(initialFile.account),
          },
        ],
        false
      );
      await Promise.resolve();
    });
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);

    await act(async () => {
      mockAuthFileLookup([refreshedFile]);
      status.reject(new Error('status failed'));
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'single.json' });
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('does not apply a successful single-item batch to a replacement identity', async () => {
    const initialFile = {
      id: 'runtime-single',
      name: 'single.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'original@example.com',
      disabled: false,
    } as AuthFileItem;
    const replacementFile = {
      ...initialFile,
      account: 'replacement@example.com',
    } as AuthFileItem;
    const status = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [replacementFile] });
    mockAuthFileLookup([initialFile]);
    mocks.setStatus.mockReturnValueOnce(status.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let mutationPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook
        .getCurrent()
        .batchSetStatus([getAuthFilePatchTarget(initialFile)], false);
      await Promise.resolve();
    });
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    await act(async () => {
      mockAuthFileLookup([replacementFile]);
      status.resolve({ status: 'ok', disabled: true });
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([replacementFile]);
    hook.unmount();
  });

  it('does not apply successful batch status to a replacement identity from a newer refresh', async () => {
    const initialFile = {
      id: 'runtime-single',
      name: 'single.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'original@example.com',
      disabled: false,
    } as AuthFileItem;
    const replacementFile = {
      ...initialFile,
      account: 'replacement@example.com',
    } as AuthFileItem;
    const status = createDeferred<{ status: string; disabled: boolean }>();
    mocks.list
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [replacementFile] });
    mockAuthFileLookup([initialFile]);
    mocks.setStatus.mockReturnValueOnce(status.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let mutationPromise!: Promise<void>;

    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook.getCurrent().batchSetStatus(
        [
          {
            name: initialFile.name,
            runtimeId: initialFile.id,
            authIndex: initialFile.auth_index as string,
            provider: 'codex',
            accountSnapshot: String(initialFile.account),
          },
        ],
        false
      );
      await Promise.resolve();
    });
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    await act(async () => {
      mockAuthFileLookup([replacementFile]);
      status.resolve({ status: 'ok', disabled: true });
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([replacementFile]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('targets one same-name credential by its unique runtime ID after refresh', async () => {
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup([{ ...files[0], disabled: true }, files[1]]);
      return { status: 'ok', disabled: true };
    });
    const onCredentialMutation = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialMutation);

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'first@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, false]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_success:1',
      'success'
    );
    expect(onCredentialMutation).toHaveBeenCalledWith({
      kind: 'status-changed',
      selectionKeys: ['shared.json\u0000auth-1'],
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('blocks a plugin source fallback when one card would change sibling credentials', async () => {
    vi.stubGlobal('localStorage', createStorage());
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    files.forEach((file) => {
      recordCodexInspectionDisableOwnership('scope-plugin-source-status', {
        fileName: file.name,
        provider: 'gemini-cli',
        authIndex: file.auth_index as string,
        accountId: null,
        accountSnapshot: String(file.account),
      });
    });
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(
      async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
        if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
        await verifyFallback();
        return {
          status: 'ok',
          disabled: true,
          mutationScope: 'source-file' as const,
        };
      }
    );
    const hook = mountUseAuthFilesData('scope-plugin-source-status');

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'first@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(5);
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'shared.json' });
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([false, false]);
    expect(
      getCodexInspectionOwnedDisableIdentityKeys('scope-plugin-source-status', [
        { ...files[0], disabled: true },
        { ...files[1], disabled: true },
      ]).size
    ).toBe(2);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    hook.unmount();
  });

  it('allows a plugin source fallback when the physical file has one credential', async () => {
    const file = {
      id: 'runtime-single',
      name: 'single.json',
      type: 'gemini-cli',
      auth_index: 'auth-1',
      account: 'single@example.com',
      disabled: false,
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [file] });
    mockAuthFileLookup([file]);
    let verifiedSourceIdentities: unknown;
    mocks.setStatus.mockImplementation(
      async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
        if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
        verifiedSourceIdentities = await verifyFallback();
        mockAuthFileLookup([{ ...file, disabled: true, status: 'disabled by source' }]);
        return {
          status: 'ok',
          disabled: true,
          mutationScope: 'source-file' as const,
        };
      }
    );
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(file)], false);
    });

    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'single.json',
        runtimeId: 'runtime-single',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'single@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files.map((item) => item.disabled)).toEqual([true]);
    expect(verifiedSourceIdentities).toEqual([
      {
        name: 'single.json',
        runtimeId: 'runtime-single',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'single@example.com',
      },
    ]);
    expect(hook.getCurrent().files[0].status).toBe('disabled by source');
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(5);
    hook.unmount();
  });

  it.each([
    'member-added',
    'member-removed',
    'identity-changed',
    'selector-collision',
    'runtime-collision',
    'runtime-id-changed',
  ] as const)(
    'fails closed when plugin source fallback verification detects %s',
    async (scenario) => {
      const files = [
        {
          id: 'runtime-shared-1',
          name: 'shared.json',
          type: 'gemini-cli',
          auth_index: 'auth-1',
          account: 'first@example.com',
          disabled: false,
        },
      ] as AuthFileItem[];
      const freshFiles =
        scenario === 'member-added'
          ? ([
              ...files,
              {
                id: 'runtime-shared-3',
                name: 'shared.json',
                type: 'gemini-cli',
                auth_index: 'auth-3',
                account: 'third@example.com',
                disabled: false,
              },
            ] as AuthFileItem[])
          : scenario === 'member-removed'
            ? []
            : scenario === 'identity-changed'
              ? [{ ...files[0], account: 'replacement@example.com' }]
              : scenario === 'runtime-id-changed'
                ? [{ ...files[0], id: 'runtime-new' }]
                : ([
                    ...files,
                    {
                      id: scenario === 'runtime-collision' ? files[0].id : 'shared.json',
                      name: 'other.json',
                      type: 'codex',
                      auth_index: 'auth-other',
                      account: 'other@example.com',
                      disabled: false,
                    },
                  ] as AuthFileItem[]);
      mocks.list.mockResolvedValueOnce({ files });
      mockAuthFileLookup(files);
      const fallbackMutation = vi.fn();
      mocks.setStatus.mockImplementation(
        async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
          if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
          mockAuthFileLookup(freshFiles);
          await verifyFallback();
          fallbackMutation();
          return {
            status: 'ok',
            disabled: true,
            mutationScope: 'source-file' as const,
          };
        }
      );
      const hook = mountUseAuthFilesData();

      await act(async () => {
        await hook.getCurrent().loadFiles();
        await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
      });

      expect(mocks.setStatus).toHaveBeenCalledTimes(1);
      expect(mocks.setStatus).toHaveBeenCalledWith(
        {
          name: 'shared.json',
          runtimeId: 'runtime-shared-1',
          authIndex: 'auth-1',
          provider: 'gemini-cli',
          accountSnapshot: 'first@example.com',
        },
        true,
        expect.any(Function)
      );
      expect(mocks.list).toHaveBeenCalledTimes(1);
      expect(mocks.lookup).toHaveBeenCalledTimes(scenario === 'runtime-id-changed' ? 6 : 5);
      expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'shared.json' });
      expect(fallbackMutation).not.toHaveBeenCalled();
      expect(hook.getCurrent().files).toEqual(
        freshFiles.filter((file) => readAuthFileStatusPhysicalName(file) === 'shared.json')
      );
      expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
        'auth_files.batch_status_partial',
        'warning'
      );
      hook.unmount();
    }
  );

  it('blocks a shared status mutation when the target has no runtime ID', async () => {
    const files = [
      {
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([false, false]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_needs_review:0/0/1',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('fails closed when a refreshed list no longer contains the original runtime ID', async () => {
    const initialFiles = [
      {
        id: 'runtime-original',
        name: 'single.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'original@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    const refreshedFiles = [
      {
        id: 'runtime-replacement',
        name: 'single.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'original@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValueOnce({ files: initialFiles });
    mockAuthFileLookup(refreshedFiles);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(initialFiles[0])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files).toEqual(refreshedFiles);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('fails closed when a refreshed target keeps its locators but changes account', async () => {
    const initialFiles = [
      {
        id: 'runtime-original',
        name: 'single.json',
        type: 'xai',
        auth_index: 'auth-1',
        account: 'original@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    const refreshedFiles = [
      {
        ...initialFiles[0],
        account: 'replacement@example.com',
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValueOnce({ files: initialFiles });
    mockAuthFileLookup(refreshedFiles);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(initialFiles[0])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files).toEqual(refreshedFiles);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('executes a source-row batch once and updates every expanded sibling', async () => {
    vi.stubGlobal('localStorage', createStorage());
    const files = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'source@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'child@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-3',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-3',
        account: 'second-child@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    files.forEach((file) => {
      recordCodexInspectionDisableOwnership('scope-source-status', {
        fileName: file.name,
        provider: 'codex',
        authIndex: file.auth_index as string,
        accountId: null,
        accountSnapshot: String(file.account),
      });
    });
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup(files.map((file) => ({ ...file, disabled: true })));
      return { status: 'ok', disabled: true };
    });
    const hook = mountUseAuthFilesData('scope-source-status');

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'shared.json',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'source@example.com',
      },
      true,
      [
        {
          name: 'shared.json',
          runtimeId: 'shared.json',
          authIndex: 'auth-1',
          provider: 'codex',
          accountSnapshot: 'source@example.com',
        },
        {
          name: 'shared.json',
          runtimeId: 'runtime-shared-2',
          authIndex: 'auth-2',
          provider: 'codex',
          accountSnapshot: 'child@example.com',
        },
        {
          name: 'shared.json',
          runtimeId: 'runtime-shared-3',
          authIndex: 'auth-3',
          provider: 'codex',
          accountSnapshot: 'second-child@example.com',
        },
      ]
    );
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, true, true]);
    expect(
      getCodexInspectionOwnedDisableIdentityKeys('scope-source-status', [
        { ...files[0], disabled: true },
        { ...files[1], disabled: true },
        { ...files[2], disabled: true },
      ])
    ).toEqual(new Set());
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('blocks an expanded child from independently changing source-file status', async () => {
    const files = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'source@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'child@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[1])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([false, false]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_needs_review:0/0/1',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('rejects refreshed auth-index drift even when the runtime ID is unchanged', async () => {
    const initialFiles = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'source@example.com',
        disabled: false,
      },
      {
        id: 'runtime-child',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'child@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    const refreshedFiles = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-source-refreshed',
        account: 'source@example.com',
        disabled: false,
      },
      initialFiles[1],
    ] as AuthFileItem[];
    mocks.list.mockResolvedValueOnce({ files: initialFiles });
    mockAuthFileLookup(refreshedFiles);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(initialFiles[0])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(hook.getCurrent().files).toEqual(refreshedFiles);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    hook.unmount();
  });

  it('fails closed for a batch target whose account snapshot changes after refresh', async () => {
    const initialFiles = [
      {
        id: 'runtime-single',
        name: 'single.json',
        type: 'xai',
        auth_index: 'auth-1',
        account: 'original@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    const refreshedFiles = [
      {
        ...initialFiles[0],
        account: 'replacement@example.com',
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValueOnce({ files: initialFiles });
    mockAuthFileLookup(refreshedFiles);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'single.json',
            authIndex: 'auth-1',
            provider: 'xai',
            accountSnapshot: 'original@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files).toEqual(refreshedFiles);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('collapses a batch source row and its expanded child into one file-level mutation', async () => {
    const files = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'source@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'child@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
    const onCredentialMutation = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialMutation);

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'shared.json',
            runtimeId: 'shared.json',
            authIndex: 'auth-1',
            provider: 'codex',
            accountSnapshot: 'source@example.com',
          },
          {
            name: 'shared.json',
            runtimeId: 'runtime-shared-2',
            authIndex: 'auth-2',
            provider: 'codex',
            accountSnapshot: 'child@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'shared.json',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'source@example.com',
      },
      true,
      [
        {
          name: 'shared.json',
          runtimeId: 'shared.json',
          authIndex: 'auth-1',
          provider: 'codex',
          accountSnapshot: 'source@example.com',
        },
        {
          name: 'shared.json',
          runtimeId: 'runtime-shared-2',
          authIndex: 'auth-2',
          provider: 'codex',
          accountSnapshot: 'child@example.com',
        },
      ]
    );
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, true]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_success:2',
      'success'
    );
    expect(onCredentialMutation).toHaveBeenCalledWith({
      kind: 'status-changed',
      selectionKeys: ['shared.json\u0000auth-1', 'shared.json\u0000auth-2'],
    });
    expect(mocks.lookup).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('blocks a partial same-file batch from widening to the plugin source', async () => {
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    mocks.setStatus.mockImplementation(
      async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
        if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
        await verifyFallback();
        return {
          status: 'ok',
          disabled: true,
          mutationScope: 'source-file' as const,
        };
      }
    );
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'shared.json',
            runtimeId: 'runtime-shared-1',
            authIndex: 'auth-1',
            provider: 'gemini-cli',
            accountSnapshot: 'first@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([false, false]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(5);
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'shared.json' });
    hook.unmount();
  });

  it('stops duplicate same-file batch mutations after a plugin source fallback succeeds', async () => {
    vi.stubGlobal('localStorage', createStorage());
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    files.forEach((file) => {
      recordCodexInspectionDisableOwnership('scope-plugin-batch-status', {
        fileName: file.name,
        provider: 'gemini-cli',
        authIndex: file.auth_index as string,
        accountId: null,
        accountSnapshot: String(file.account),
      });
    });
    mocks.list.mockResolvedValue({ files });
    mocks.setStatus.mockResolvedValue({
      status: 'ok',
      disabled: true,
      mutationScope: 'source-file',
    });
    const hook = mountUseAuthFilesData('scope-plugin-batch-status');

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        files.map((file) => ({
          name: file.name,
          runtimeId: file.id,
          authIndex: file.auth_index as string,
          provider: 'gemini-cli',
          accountSnapshot: String(file.account),
        })),
        false
      );
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'first@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, true]);
    expect(
      getCodexInspectionOwnedDisableIdentityKeys('scope-plugin-batch-status', [
        { ...files[0], disabled: true },
        { ...files[1], disabled: true },
      ])
    ).toEqual(new Set());
    expect(mocks.lookup).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('lets a successful source-file result supersede an earlier same-file failure', async () => {
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-1',
        account: 'first@example.com',
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'gemini-cli',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    let attempt = 0;
    mocks.setStatus.mockImplementation(
      async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
        attempt++;
        if (attempt === 1) throw new Error('transient credential failure');
        if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
        await verifyFallback();
        return {
          status: 'ok',
          disabled: true,
          mutationScope: 'source-file' as const,
        };
      }
    );
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        files.map((file) => ({
          name: file.name,
          runtimeId: file.id,
          authIndex: file.auth_index as string,
          provider: 'gemini-cli',
          accountSnapshot: String(file.account),
        })),
        false
      );
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(2);
    expect(mocks.setStatus).toHaveBeenNthCalledWith(
      1,
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'gemini-cli',
        accountSnapshot: 'first@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(mocks.setStatus).toHaveBeenNthCalledWith(
      2,
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-2',
        authIndex: 'auth-2',
        provider: 'gemini-cli',
        accountSnapshot: 'second@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(mocks.list).toHaveBeenCalledTimes(3);
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, true]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_success:2',
      'success'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(mocks.lookup).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('executes same-name batch targets independently by current runtime ID', async () => {
    vi.stubGlobal('localStorage', createStorage());
    const files = [
      {
        id: 'runtime-shared-1',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        id_token: { account_id: 'account-1' },
        disabled: false,
      },
      {
        id: 'runtime-shared-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account: 'second@example.com',
        disabled: false,
      },
      {
        id: 'runtime-single-3',
        name: 'single.json',
        type: 'codex',
        auth_index: 'auth-3',
        account: 'third@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    files.slice(0, 2).forEach((file) => {
      recordCodexInspectionDisableOwnership('scope-status', {
        fileName: file.name,
        provider: 'codex',
        authIndex: file.auth_index as string,
        accountId: file.auth_index === 'auth-1' ? 'account-1' : null,
        accountSnapshot: file.auth_index === 'auth-1' ? null : String(file.account),
      });
    });
    mocks.list.mockResolvedValue({ files });
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
    const hook = mountUseAuthFilesData('scope-status');

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    await act(async () => {
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'shared.json',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'account-1',
          },
          {
            name: 'shared.json',
            authIndex: 'auth-2',
            provider: 'codex',
            accountSnapshot: 'second@example.com',
          },
          {
            name: 'single.json',
            authIndex: 'auth-3',
            provider: 'codex',
            accountSnapshot: 'third@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(3);
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
      },
      true,
      expect.any(Function)
    );
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'shared.json',
        runtimeId: 'runtime-shared-2',
        authIndex: 'auth-2',
        provider: 'codex',
        accountSnapshot: 'second@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(mocks.setStatus).toHaveBeenCalledWith(
      {
        name: 'single.json',
        runtimeId: 'runtime-single-3',
        authIndex: 'auth-3',
        provider: 'codex',
        accountSnapshot: 'third@example.com',
      },
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files.map((file) => [file.auth_index, file.disabled])).toEqual([
      ['auth-1', true],
      ['auth-2', true],
      ['auth-3', true],
    ]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_success:3',
      'success'
    );

    const ownedKeys = getCodexInspectionOwnedDisableIdentityKeys('scope-status', [
      { ...files[0], disabled: true },
      { ...files[1], disabled: true },
    ]);
    expect(ownedKeys).toEqual(new Set());
    expect(mocks.lookup).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('keeps unindexed same-file batch identities distinct when refresh is ambiguous', async () => {
    const files = [
      {
        name: 'shared.json',
        type: 'xai',
        account: 'first@example.com',
        disabled: false,
      },
      {
        name: 'shared.json',
        type: 'xai',
        account: 'second@example.com',
        disabled: false,
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus(
        [
          {
            name: 'shared.json',
            provider: 'xai',
            accountSnapshot: 'first@example.com',
          },
          {
            name: 'shared.json',
            provider: 'xai',
            accountSnapshot: 'second@example.com',
          },
        ],
        false
      );
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_needs_review:0/0/2',
      'warning'
    );
    expect(mocks.lookup).not.toHaveBeenCalled();
    hook.unmount();
  });
});

describe('useAuthFilesData scoped status snapshots', () => {
  const targetFile: AuthFileItem = {
    name: 'target.json',
    id: 'runtime-target',
    auth_index: 'auth-target',
    type: 'codex',
    account_id: 'workspace-target',
    account: 'target@example.com',
    disabled: false,
  };
  const unrelatedFile: AuthFileItem = {
    name: 'unrelated.json',
    id: 'runtime-unrelated',
    auth_index: 'auth-unrelated',
    type: 'codex',
    account: 'unrelated@example.com',
    disabled: false,
  };

  it.each([true, false])(
    'reconciles only the server source after setting enabled=%s',
    async (enabled) => {
      const initialFile = { ...targetFile, disabled: enabled };
      const readBack = {
        ...initialFile,
        disabled: !enabled,
        status: 'server status',
        note: 'fresh',
      };
      const requestScope = { apiBase: 'http://cpa-a.local:8317', managementKey: 'test-key' };
      const onCredentialMutation = vi.fn();
      mocks.list.mockResolvedValue({ files: [unrelatedFile, initialFile] });
      mockAuthFileLookup([unrelatedFile, initialFile]);
      mocks.setStatus.mockImplementation(async () => {
        mockAuthFileLookup([unrelatedFile, readBack]);
        return { status: 'ok', disabled: !enabled, mutationScope: 'credential' };
      });
      const hook = mountUseAuthFilesData(
        'connection-a',
        undefined,
        onCredentialMutation,
        requestScope
      );
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });
      mocks.list.mockClear();

      await act(async () => {
        await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(initialFile)], enabled);
      });

      expect(mocks.list).not.toHaveBeenCalled();
      expect(mocks.lookup.mock.calls).toEqual([
        [{ name: 'target.json' }, requestScope],
        [{ name: 'runtime-target' }, requestScope],
        [{ name: 'target.json' }, requestScope],
      ]);
      expect(mocks.setStatus).toHaveBeenCalledExactlyOnceWith(
        getAuthFilePatchTarget(initialFile),
        !enabled,
        expect.any(Function),
        requestScope
      );
      expect(hook.getCurrent().files).toEqual([unrelatedFile, readBack]);
      expect(onCredentialMutation).toHaveBeenCalledExactlyOnceWith({
        kind: 'status-changed',
        selectionKeys: ['target.json\u0000auth-target'],
      });
      expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
        'auth_files.batch_status_success:1',
        'success'
      );
      expect(hook.getCurrent().batchStatusUpdating).toBe(false);
      hook.unmount();
    }
  );

  it('preserves fresh siblings that were absent from the page snapshot', async () => {
    const sibling = {
      ...targetFile,
      id: 'runtime-sibling',
      auth_index: 'auth-sibling',
      account: 'sibling@example.com',
    };
    const readBack = [
      { ...targetFile, disabled: true },
      { ...sibling, note: 'server sibling' },
    ];
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([targetFile, sibling]);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup(readBack);
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.setStatus).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(targetFile),
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files).toEqual([unrelatedFile, ...readBack]);
    hook.unmount();
  });

  it('blocks a child when fresh source membership introduces a source row', async () => {
    const source = {
      ...targetFile,
      id: 'target.json',
      auth_index: 'source',
      account: 'source@example.com',
    };
    mocks.list.mockResolvedValue({ files: [targetFile] });
    mockAuthFileLookup([source, targetFile]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(mocks.lookup.mock.calls).toEqual([
      [{ name: 'target.json' }],
      [{ name: 'runtime-target' }],
    ]);
    expect(hook.getCurrent().files).toEqual([source, targetFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_needs_review:0/0/1',
      'warning'
    );
    hook.unmount();
  });

  it.each([
    { id: 'target.json', runtimeId: 'runtime-target' },
    { id: 'runtime-target', runtimeId: 'runtime-target' },
    { id: 'runtime-target', runtimeId: null },
  ])('rejects cross-source selector collisions: %j', async ({ id, runtimeId }) => {
    const collision = { ...unrelatedFile, name: 'collision.json', id };
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([targetFile, collision]);
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    await act(async () => {
      await hook
        .getCurrent()
        .batchSetStatus([{ ...getAuthFilePatchTarget(targetFile), runtimeId }], false);
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(mocks.lookup).toHaveBeenCalledWith({ name: 'target.json' });
    expect(mocks.lookup).toHaveBeenCalledWith({ name: 'runtime-target' });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, targetFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_needs_review:0/0/1',
      'warning'
    );
    hook.unmount();
  });

  it('rejects a changed account ID even when source, runtime and auth index still match', async () => {
    const replacement = { ...targetFile, account_id: 'replacement-workspace' };
    mocks.list.mockResolvedValue({ files: [targetFile] });
    mockAuthFileLookup([replacement]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().files).toEqual([replacement]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    hook.unmount();
  });

  it('reconciles server state after a status response is lost without reclassifying the failure', async () => {
    const readBack = {
      ...targetFile,
      disabled: true,
      status: 'disabled on server',
      note: 'server-only metadata',
    };
    const onCredentialMutation = vi.fn();
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup([readBack]);
      throw new Error('response lost');
    });
    const hook = mountUseAuthFilesData('connection-a', undefined, onCredentialMutation);
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.setStatus).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(targetFile),
      true,
      expect.any(Function)
    );
    await expect(mocks.setStatus.mock.results[0].value).rejects.toThrow('response lost');
    expect(mocks.lookup.mock.calls).toEqual([
      [{ name: 'target.json' }],
      [{ name: 'runtime-target' }],
      [{ name: 'target.json' }],
    ]);
    expect(hook.getCurrent().files).toEqual([unrelatedFile, readBack]);
    expect(onCredentialMutation).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
      'auth_files.batch_status_partial',
      'warning'
    );
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('preserves local state and both warnings when a status response is lost and read-back fails', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mocks.lookup.mockRejectedValue(new Error('source read-back failed'));
      throw new Error('response lost');
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.setStatus).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(targetFile),
      true,
      expect.any(Function)
    );
    await expect(mocks.setStatus.mock.results[0].value).rejects.toThrow('response lost');
    expect(mocks.lookup.mock.calls).toEqual([
      [{ name: 'target.json' }],
      [{ name: 'runtime-target' }],
      [{ name: 'target.json' }],
    ]);
    expect(hook.getCurrent().files).toEqual([unrelatedFile, targetFile]);
    expect(mocks.showNotification.mock.calls).toEqual([
      ['auth_files.batch_status_partial', 'warning'],
      ['notification.refresh_failed: source read-back failed', 'warning'],
    ]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('retains confirmed success and warns when the source read-back fails', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mocks.lookup.mockRejectedValue(new Error('source read-back failed'));
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'target.json' });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, { ...targetFile, disabled: true }]);
    expect(mocks.showNotification.mock.calls).toEqual([
      ['auth_files.batch_status_success:1', 'success'],
      ['notification.refresh_failed: source read-back failed', 'warning'],
    ]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('holds the original pending lock until scoped read-back completes', async () => {
    const readBack = createDeferred<AuthFileItem[]>();
    mocks.list.mockResolvedValue({ files: [targetFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mocks.lookup.mockReturnValueOnce(readBack.promise);
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    let mutation!: Promise<void>;
    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutation = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    expect(hook.getCurrent().files[0].disabled).toBe(true);
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], true);
    });

    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);
    await act(async () => {
      readBack.resolve([{ ...targetFile, disabled: true, note: 'read-back' }]);
      await mutation;
    });
    expect(hook.getCurrent().files[0].note).toBe('read-back');
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it('rereads the source once instead of overwriting a newer revision with a stale read-back', async () => {
    const readBack = createDeferred<AuthFileItem[]>();
    const newerFile = { ...targetFile, note: 'newer status mutation', disabled: false };
    mocks.list
      .mockResolvedValueOnce({ files: [targetFile] })
      .mockResolvedValueOnce({ files: [newerFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mocks.lookup.mockReturnValueOnce(readBack.promise);
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    let mutation!: Promise<void>;
    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutation = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mockAuthFileLookup([newerFile]);

    await act(async () => {
      readBack.resolve([{ ...targetFile, disabled: true }]);
      await mutation;
    });

    expect(mocks.lookup).toHaveBeenCalledTimes(4);
    expect(mocks.lookup).toHaveBeenLastCalledWith({ name: 'target.json' });
    expect(hook.getCurrent().files).toEqual([newerFile]);
    expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
      'auth_files.batch_status_success:1',
      'success'
    );
    hook.unmount();
  });

  it('keeps the newest revision and warns if the one allowed retry is also stale', async () => {
    const firstRead = createDeferred<AuthFileItem[]>();
    const retryRead = createDeferred<AuthFileItem[]>();
    const newerFile = { ...targetFile, note: 'newer' };
    const latestFile = { ...targetFile, note: 'latest' };
    mocks.list
      .mockResolvedValueOnce({ files: [targetFile] })
      .mockResolvedValueOnce({ files: [newerFile] })
      .mockResolvedValueOnce({ files: [latestFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mocks.lookup.mockReturnValueOnce(firstRead.promise).mockReturnValueOnce(retryRead.promise);
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    let mutation!: Promise<void>;
    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutation = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
    });
    await act(async () => {
      await hook.getCurrent().loadFiles();
      firstRead.resolve([{ ...targetFile, disabled: true }]);
    });
    expect(mocks.lookup).toHaveBeenCalledTimes(4);
    await act(async () => {
      await hook.getCurrent().loadFiles();
      retryRead.resolve([newerFile]);
      await mutation;
    });

    expect(mocks.lookup).toHaveBeenCalledTimes(4);
    expect(hook.getCurrent().files).toEqual([latestFile]);
    expect(mocks.showNotification.mock.calls).toEqual([
      ['auth_files.batch_status_success:1', 'success'],
      ['notification.refresh_failed', 'warning'],
    ]);
    expect(hook.getCurrent().batchStatusUpdating).toBe(false);
    hook.unmount();
  });

  it.each([
    ['preflight', false],
    ['preflight', true],
    ['fallback', false],
    ['fallback', true],
    ['read-back', false],
    ['read-back', true],
  ] as const)(
    'ignores an old connection %s lookup (reject=%s) while the new mutation is pending',
    async (phase, reject) => {
      const oldRead = createDeferred<AuthFileItem[]>();
      const newStatus = createDeferred<{ status: string; disabled: boolean }>();
      const newFile = { ...targetFile, account: 'connection-b@example.com', disabled: true };
      let onNewConnection = false;
      const fallbackMutation = vi.fn();
      mocks.list
        .mockResolvedValueOnce({ files: [targetFile] })
        .mockResolvedValueOnce({ files: [newFile] });
      mockAuthFileLookup([targetFile]);
      if (phase === 'preflight') mocks.lookup.mockReturnValueOnce(oldRead.promise);
      mocks.setStatus.mockImplementation(
        async (_target, _disabled, verifyFallback?: () => Promise<unknown>) => {
          if (onNewConnection) return newStatus.promise;
          mocks.lookup.mockReturnValueOnce(oldRead.promise);
          if (phase === 'fallback') {
            await verifyFallback!();
            fallbackMutation();
          }
          return { status: 'ok', disabled: true, mutationScope: 'credential' };
        }
      );
      const hook = mountUseAuthFilesData('connection-a');
      let oldMutation!: Promise<void>;
      await act(async () => {
        await hook.getCurrent().loadFiles();
        oldMutation = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
      });
      expect(mocks.lookup).toHaveBeenCalledTimes(
        phase === 'preflight' ? 2 : phase === 'fallback' ? 4 : 3
      );

      hook.rerender('connection-b');
      onNewConnection = true;
      mockAuthFileLookup([newFile]);
      mocks.showNotification.mockClear();
      let newMutation!: Promise<void>;
      await act(async () => {
        await hook.getCurrent().loadFiles();
        newMutation = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(newFile)], true);
      });
      expect(hook.getCurrent().batchStatusUpdating).toBe(true);

      await act(async () => {
        if (reject) oldRead.reject(new Error('old connection failed'));
        else oldRead.resolve([targetFile]);
        await oldMutation;
      });

      expect(hook.getCurrent().files).toEqual([newFile]);
      expect(hook.getCurrent().batchStatusUpdating).toBe(true);
      expect(mocks.showNotification).not.toHaveBeenCalled();
      expect(fallbackMutation).not.toHaveBeenCalled();
      expect(mocks.setStatus).toHaveBeenCalledTimes(phase === 'preflight' ? 1 : 2);
      await act(async () => {
        mockAuthFileLookup([{ ...newFile, disabled: false }]);
        newStatus.resolve({ status: 'ok', disabled: false });
        await newMutation;
      });
      expect(hook.getCurrent().files).toEqual([{ ...newFile, disabled: false }]);
      expect(hook.getCurrent().batchStatusUpdating).toBe(false);
      expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
        'auth_files.batch_status_success:1',
        'success'
      );
      hook.unmount();
    }
  );

  it.each(['source', 'identity'])(
    'does not mutate when the scoped %s preflight fails',
    async (scope) => {
      mocks.list.mockResolvedValue({ files: [targetFile] });
      mocks.lookup.mockImplementation(async ({ name }: AuthFileLookupTarget) => {
        if (name === (scope === 'source' ? 'target.json' : 'runtime-target')) {
          throw new Error('lookup failed');
        }
        return [targetFile];
      });
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
        await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(targetFile)], false);
      });

      expect(mocks.list).toHaveBeenCalledTimes(1);
      expect(mocks.setStatus).not.toHaveBeenCalled();
      expect(mocks.lookup.mock.calls).toEqual([
        [{ name: 'target.json' }],
        [{ name: 'runtime-target' }],
      ]);
      expect(hook.getCurrent().files).toEqual([targetFile]);
      expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
        'notification.update_failed: lookup failed',
        'error'
      );
      expect(hook.getCurrent().batchStatusUpdating).toBe(false);
      hook.unmount();
    }
  );

  it('uses the scoped path when normalization deduplicates repeated targets', async () => {
    mocks.list.mockResolvedValue({ files: [targetFile] });
    mockAuthFileLookup([targetFile]);
    mocks.setStatus.mockImplementation(async () => {
      mockAuthFileLookup([{ ...targetFile, disabled: true }]);
      return { status: 'ok', disabled: true, mutationScope: 'credential' };
    });
    const hook = mountUseAuthFilesData();
    const target = getAuthFilePatchTarget(targetFile);
    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([target, target], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.lookup).toHaveBeenCalledTimes(3);
    expect(mocks.setStatus).toHaveBeenCalledTimes(1);
    expect(hook.getCurrent().files[0].disabled).toBe(true);
    expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
      'auth_files.batch_status_success:1',
      'success'
    );
    hook.unmount();
  });

  it('keeps multi-target full-list collision checks and only mutates the safe target', async () => {
    const collision = { ...targetFile, name: 'collision.json', id: 'target.json' };
    mocks.list
      .mockResolvedValueOnce({ files: [targetFile, unrelatedFile] })
      .mockResolvedValueOnce({ files: [targetFile, unrelatedFile, collision] });
    mocks.setStatus.mockResolvedValue({
      status: 'ok',
      disabled: true,
      mutationScope: 'credential',
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook
        .getCurrent()
        .batchSetStatus(
          [getAuthFilePatchTarget(targetFile), getAuthFilePatchTarget(unrelatedFile)],
          false
        );
    });

    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.lookup).not.toHaveBeenCalled();
    expect(mocks.setStatus).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(unrelatedFile),
      true,
      expect.any(Function)
    );
    expect(hook.getCurrent().files).toEqual([
      targetFile,
      { ...unrelatedFile, disabled: true },
      collision,
    ]);
    expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
      'auth_files.batch_status_needs_review:1/0/1',
      'warning'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData handleCredentialRefresh', () => {
  it('waits a full timestamp tick before refreshing a credential updated in the same second', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T00:00:00.100Z'));
    const file: AuthFileItem = {
      id: 'codex-runtime-auth-id',
      name: 'codex-account.json',
      authIndex: 'auth-1',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
    };
    const refreshedFile = {
      ...file,
      last_refresh: '2026-01-01T00:00:01Z',
    } as AuthFileItem;
    mocks.lookup.mockResolvedValueOnce([file]).mockResolvedValue([refreshedFile]);
    const hook = mountUseAuthFilesData();
    let request!: Promise<void>;

    await act(async () => {
      request = hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    expect(mocks.requestCredentialRefresh).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(mocks.requestCredentialRefresh).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
      await request;
    });

    expect(mocks.requestCredentialRefresh).toHaveBeenCalledTimes(1);
    expect(hook.getCurrent().files).toEqual([refreshedFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_completed:codex-account.json',
      'success'
    );
    hook.unmount();
  });

  it('tracks the exact auth row until CPA confirms the refreshed credential', async () => {
    let resolveRequest!: () => void;
    mocks.requestCredentialRefresh.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveRequest = resolve;
        })
    );
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
    const file: AuthFileItem = {
      id: 'codex-runtime-auth-id',
      name: 'shared-codex.json',
      authIndex: 'auth-2',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
      id_token: { plan_type: 'free' },
    };
    const refreshedFiles: AuthFileItem[] = [
      {
        id: 'codex-runtime-auth-1',
        name: 'shared-codex.json',
        authIndex: 'auth-1',
        type: 'codex',
        last_refresh: '2026-01-01T00:00:00Z',
        id_token: { plan_type: 'free' },
      },
      {
        ...file,
        last_refresh: '2026-01-02T00:00:00Z',
        id_token: { plan_type: 'plus' },
      },
    ];
    mocks.lookup
      .mockResolvedValueOnce([refreshedFiles[0], file])
      .mockResolvedValueOnce([refreshedFiles[1]])
      .mockResolvedValue(refreshedFiles);
    const operationKey = 'shared-codex.json\u0000auth-2';
    let request!: Promise<void>;

    await act(async () => {
      request = hook.getCurrent().handleCredentialRefresh(file);
      void hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    expect(mocks.requestCredentialRefresh).toHaveBeenCalledWith(
      {
        name: 'shared-codex.json',
        runtimeId: 'codex-runtime-auth-id',
        authIndex: 'auth-2',
        provider: 'codex',
      },
      [
        {
          name: 'shared-codex.json',
          runtimeId: 'codex-runtime-auth-1',
          authIndex: 'auth-1',
          provider: 'codex',
        },
        {
          name: 'shared-codex.json',
          runtimeId: 'codex-runtime-auth-id',
          authIndex: 'auth-2',
          provider: 'codex',
        },
      ]
    );
    expect(mocks.requestCredentialRefresh).toHaveBeenCalledTimes(1);
    expect(hook.getCurrent().credentialRefreshing[operationKey]).toBe(true);

    await act(async () => {
      resolveRequest();
      await request;
    });

    expect(hook.getCurrent().credentialRefreshing[operationKey]).toBeUndefined();
    expect(hook.getCurrent().files).toEqual(refreshedFiles);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_completed:shared-codex.json',
      'success'
    );
    expect(onCredentialFilesChanged).toHaveBeenCalledWith({
      kind: 'credential-refreshed',
      selectionKeys: ['shared-codex.json\u0000auth-2'],
    });
    hook.unmount();
  });

  it('reconciles the refreshed source without replacing unrelated account rows', async () => {
    const unrelated = {
      id: 'claude-runtime-auth-id',
      name: 'claude-account.json',
      authIndex: 'claude-auth-1',
      type: 'claude',
      note: 'keep-me',
    } as AuthFileItem;
    const file = {
      id: 'codex-runtime-auth-id',
      name: 'codex-account.json',
      authIndex: 'auth-1',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
    } as AuthFileItem;
    const refreshedFile = {
      ...file,
      last_refresh: '2026-01-02T00:00:00Z',
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [unrelated, file] });
    mocks.lookup
      .mockResolvedValueOnce([file])
      .mockResolvedValueOnce([file])
      .mockResolvedValue([refreshedFile]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().handleCredentialRefresh(file);
    });

    expect(hook.getCurrent().files).toEqual([unrelated, refreshedFile]);
    expect(mocks.lookup).toHaveBeenNthCalledWith(1, { name: 'codex-account.json' });
    expect(mocks.lookup).toHaveBeenNthCalledWith(3, {
      name: 'codex-account.json',
      authIndex: 'auth-1',
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('keeps the request pending when CPA does not confirm the refresh in time', async () => {
    vi.useFakeTimers();
    const file: AuthFileItem = {
      id: 'codex-runtime-auth-id',
      name: 'codex-account.json',
      authIndex: 'auth-1',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
      id_token: { plan_type: 'free' },
    };
    mocks.lookup.mockResolvedValue([file]);
    const onCredentialFilesChanged = vi.fn();
    const hook = mountUseAuthFilesData(undefined, undefined, onCredentialFilesChanged);
    let request!: Promise<void>;

    await act(async () => {
      request = hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    expect(hook.getCurrent().credentialRefreshing['codex-account.json\u0000auth-1']).toBe(true);

    await act(async () => {
      await vi.runAllTimersAsync();
      await request;
    });

    expect(mocks.lookup).toHaveBeenCalledTimes(18);
    expect(mocks.list).not.toHaveBeenCalled();
    expect(hook.getCurrent().files).toEqual([file]);
    expect(
      hook.getCurrent().credentialRefreshing['codex-account.json\u0000auth-1']
    ).toBeUndefined();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_pending:codex-account.json',
      'warning'
    );
    expect(onCredentialFilesChanged).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('does not accept a refreshed timestamp from a replacement credential', async () => {
    vi.useFakeTimers();
    const original = {
      id: 'runtime-auth-1',
      name: 'same.json',
      authIndex: 'auth-1',
      type: 'codex',
      account_id: 'original-account',
      last_refresh: '2026-01-01T00:00:00Z',
    } as AuthFileItem;
    const replacement = {
      ...original,
      id: 'runtime-auth-2',
      account_id: 'replacement-account',
      last_refresh: '2026-01-02T00:00:00Z',
    } as AuthFileItem;
    mocks.lookup
      .mockResolvedValueOnce([original])
      .mockResolvedValueOnce([original])
      .mockResolvedValue([replacement]);
    const hook = mountUseAuthFilesData();
    let request!: Promise<void>;

    await act(async () => {
      request = hook.getCurrent().handleCredentialRefresh(original);
      await Promise.resolve();
    });
    await act(async () => {
      await vi.runAllTimersAsync();
      await request;
    });

    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.credential_refresh_completed:same.json',
      'success'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_pending:same.json',
      'warning'
    );
    hook.unmount();
  });

  it('does not let an old connection cleanup unlock the same row on a new connection', async () => {
    let resolveFirst!: () => void;
    let resolveSecond!: () => void;
    mocks.requestCredentialRefresh
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveFirst = resolve;
          })
      )
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveSecond = resolve;
          })
      );
    const file: AuthFileItem = {
      id: 'codex-runtime-auth-id',
      name: 'codex-account.json',
      authIndex: 'auth-1',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
    };
    mocks.lookup
      .mockResolvedValueOnce([file])
      .mockResolvedValueOnce([file])
      .mockResolvedValueOnce([file])
      .mockResolvedValueOnce([file])
      .mockResolvedValue([{ ...file, last_refresh: '2026-01-02T00:00:00Z' }]);
    const hook = mountUseAuthFilesData('connection-a');
    let firstRequest!: Promise<void>;
    let secondRequest!: Promise<void>;

    await act(async () => {
      firstRequest = hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    hook.rerender('connection-b');

    await act(async () => {
      secondRequest = hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    await act(async () => {
      resolveFirst();
      await firstRequest;
    });

    await act(async () => {
      await hook.getCurrent().handleCredentialRefresh(file);
    });
    expect(mocks.requestCredentialRefresh).toHaveBeenCalledTimes(2);

    await act(async () => {
      resolveSecond();
      await secondRequest;
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_completed:codex-account.json',
      'success'
    );
    hook.unmount();
  });

  it('invalidates the old credential-refresh lock before new-connection layout work runs', async () => {
    const firstRequest = createDeferred<void>();
    const secondRequest = createDeferred<void>();
    const file: AuthFileItem = {
      id: 'codex-runtime-auth-id',
      name: 'codex-account.json',
      authIndex: 'auth-1',
      type: 'codex',
      last_refresh: '2026-01-01T00:00:00Z',
    };
    mocks.lookup.mockResolvedValue([file]);
    mocks.requestCredentialRefresh
      .mockReturnValueOnce(firstRequest.promise)
      .mockReturnValueOnce(secondRequest.promise);
    let layoutRequest: Promise<void> | undefined;
    const hook = mountUseAuthFilesData('connection-a', (value, fingerprint) => {
      if (fingerprint !== 'connection-b') return;
      layoutRequest = value.handleCredentialRefresh(file);
    });
    let initialRequest!: Promise<void>;

    await act(async () => {
      initialRequest = hook.getCurrent().handleCredentialRefresh(file);
      await Promise.resolve();
    });

    hook.rerender('connection-b');

    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.requestCredentialRefresh).toHaveBeenCalledTimes(2);
    expect(layoutRequest).toBeDefined();

    await act(async () => {
      mocks.lookup.mockResolvedValue([{ ...file, last_refresh: '2026-01-02T00:00:00Z' }]);
      firstRequest.resolve();
      secondRequest.resolve();
      await Promise.all([initialRequest, layoutRequest]);
    });
    hook.unmount();
  });

  it('reports request failures after resolving the current credential identity', async () => {
    mocks.requestCredentialRefresh.mockRejectedValue(new Error('refresh unavailable'));
    const file = {
      id: 'single-runtime-id',
      name: 'single-codex.json',
      authIndex: 'auth-1',
      type: 'codex',
    } as AuthFileItem;
    mocks.lookup.mockResolvedValue([file]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().handleCredentialRefresh(file);
    });

    expect(mocks.requestCredentialRefresh).toHaveBeenCalledWith(
      {
        name: 'single-codex.json',
        runtimeId: 'single-runtime-id',
        authIndex: 'auth-1',
        provider: 'codex',
      },
      [
        {
          name: 'single-codex.json',
          runtimeId: 'single-runtime-id',
          authIndex: 'auth-1',
          provider: 'codex',
        },
      ]
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_failed:single-codex.json',
      'error'
    );
    hook.unmount();
  });

  it('rejects a replacement credential before requesting a refresh', async () => {
    const original = {
      id: 'runtime-auth-1',
      name: 'same.json',
      authIndex: 'auth-1',
      type: 'codex',
      account_id: 'original-account',
    } as AuthFileItem;
    mocks.lookup.mockResolvedValue([{ ...original, account_id: 'replacement-account' }]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().handleCredentialRefresh(original);
    });

    expect(mocks.requestCredentialRefresh).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_failed:same.json',
      'error'
    );
    hook.unmount();
  });

  it('rejects a runtime identity collision outside the target source', async () => {
    const original = {
      id: 'runtime-auth-1',
      name: 'source-a.json',
      authIndex: 'auth-1',
      type: 'codex',
      account_id: 'account-a',
    } as AuthFileItem;
    const collision = {
      id: 'runtime-auth-1',
      name: 'source-b.json',
      authIndex: 'auth-2',
      type: 'codex',
      account_id: 'account-b',
    } as AuthFileItem;
    mocks.lookup.mockResolvedValueOnce([original]).mockResolvedValueOnce([original, collision]);
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().handleCredentialRefresh(original);
    });

    expect(mocks.lookup).toHaveBeenNthCalledWith(1, { name: 'source-a.json' });
    expect(mocks.lookup).toHaveBeenNthCalledWith(2, { name: 'runtime-auth-1' });
    expect(mocks.requestCredentialRefresh).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.credential_refresh_failed:source-a.json',
      'error'
    );
    hook.unmount();
  });
});

describe('useAuthFilesData batchPatchFields', () => {
  const targetFile: AuthFileItem = {
    name: 'target.json',
    id: 'runtime-target',
    auth_index: 'auth-target',
    type: 'codex',
    account_id: 'workspace-target',
    account: 'target@example.com',
    priority: 1,
    note: '',
    websockets: true,
  };
  const unrelatedFile: AuthFileItem = {
    name: 'unrelated.json',
    id: 'runtime-unrelated',
    auth_index: 'auth-unrelated',
    type: 'codex',
    account_id: 'workspace-unrelated',
    account: 'unrelated@example.com',
    priority: 1,
    note: '',
    websockets: true,
  };

  it('does not let old connection cleanup clear a new batch-fields operation', async () => {
    const oldFile = {
      id: 'runtime-shared',
      name: 'shared.json',
      type: 'codex',
      auth_index: 'auth-1',
      account: 'old@example.com',
    } as AuthFileItem;
    const newFile = { ...oldFile, account: 'new@example.com' } as AuthFileItem;
    const oldPatch = createDeferred<void>();
    const newPatch = createDeferred<void>();
    mocks.list
      .mockResolvedValueOnce({ files: [oldFile] })
      .mockResolvedValueOnce({ files: [newFile] })
      .mockResolvedValueOnce({ files: [newFile] });
    mockAuthFileLookup([oldFile]);
    mocks.patchFieldsWithPluginSourceFallback
      .mockReturnValueOnce(oldPatch.promise)
      .mockReturnValueOnce(newPatch.promise);
    const hook = mountUseAuthFilesData('connection-a');
    let oldPromise!: ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>;
    let newPromise!: ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>;

    await act(async () => {
      oldPromise = hook.getCurrent().batchPatchFields(
        [
          {
            name: oldFile.name,
            runtimeId: oldFile.id,
            authIndex: oldFile.auth_index as string,
            provider: 'codex',
            accountSnapshot: String(oldFile.account),
          },
        ],
        { priority: 1 }
      );
      await Promise.resolve();
    });
    expect(hook.getCurrent().batchFieldsUpdating).toBe(true);

    hook.rerender('connection-b');
    expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
    mockAuthFileLookup([newFile]);
    await act(async () => {
      newPromise = hook.getCurrent().batchPatchFields(
        [
          {
            name: newFile.name,
            runtimeId: newFile.id,
            authIndex: newFile.auth_index as string,
            provider: 'codex',
            accountSnapshot: String(newFile.account),
          },
        ],
        { priority: 2 }
      );
      await Promise.resolve();
    });
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledTimes(2);
    expect(hook.getCurrent().batchFieldsUpdating).toBe(true);

    let oldResult: Awaited<typeof oldPromise>;
    await act(async () => {
      oldPatch.resolve();
      oldResult = await oldPromise;
    });
    expect(oldResult!).toBeNull();
    expect(hook.getCurrent().files).toEqual([newFile]);
    expect(hook.getCurrent().batchFieldsUpdating).toBe(true);
    expect(mocks.showNotification).not.toHaveBeenCalled();

    let newResult: Awaited<typeof newPromise>;
    await act(async () => {
      newPatch.resolve();
      newResult = await newPromise;
    });
    expect(newResult!).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
    expect(mocks.showNotification).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('patches selected auth indexes from the same file in one request', async () => {
    const files = [
      {
        id: 'runtime-auth-1',
        name: 'shared-codex.json',
        authIndex: 'auth-1',
        type: 'codex',
        account_id: 'account-1',
      },
      {
        id: 'runtime-auth-2',
        name: 'shared-codex.json',
        authIndex: 'auth-2',
        type: 'codex',
        account_id: 'account-2',
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [
          {
            name: 'shared-codex.json',
            runtimeId: 'runtime-auth-1',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'account-1',
          },
          {
            name: 'shared-codex.json',
            runtimeId: 'runtime-auth-2',
            authIndex: 'auth-2',
            provider: 'codex',
            accountId: 'account-2',
          },
          {
            name: 'shared-codex.json',
            runtimeId: 'runtime-auth-1',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'account-1',
          },
        ],
        { priority: 10 }
      );
    });

    expect(mocks.patchFieldsForAuthIndexes).toHaveBeenCalledWith(
      'shared-codex.json',
      [
        {
          name: 'shared-codex.json',
          runtimeId: 'runtime-auth-1',
          authIndex: 'auth-1',
          provider: 'codex',
          accountId: 'account-1',
        },
        {
          name: 'shared-codex.json',
          runtimeId: 'runtime-auth-2',
          authIndex: 'auth-2',
          provider: 'codex',
          accountId: 'account-2',
        },
      ],
      [
        {
          name: 'shared-codex.json',
          runtimeId: 'runtime-auth-1',
          authIndex: 'auth-1',
          provider: 'codex',
          accountId: 'account-1',
        },
        {
          name: 'shared-codex.json',
          runtimeId: 'runtime-auth-2',
          authIndex: 'auth-2',
          provider: 'codex',
          accountId: 'account-2',
        },
      ],
      { priority: 10 }
    );
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 2, failed: 0, failedNames: [] });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_fields_success',
      'success'
    );
    hook.unmount();
  });

  it('patches a single verified credential without widening to its physical file', async () => {
    const file = {
      id: 'single-runtime-id',
      name: 'single-codex.json',
      type: 'codex',
      account: 'single@example.com',
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [file] });
    mockAuthFileLookup([file]);
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [
          {
            name: 'single-codex.json',
            runtimeId: 'single-runtime-id',
            provider: 'codex',
            accountSnapshot: 'single@example.com',
          },
        ],
        { websockets: false }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      {
        name: 'single-codex.json',
        runtimeId: 'single-runtime-id',
        provider: 'codex',
        accountSnapshot: 'single@example.com',
      },
      { websockets: false },
      [
        {
          name: 'single-codex.json',
          runtimeId: 'single-runtime-id',
          provider: 'codex',
          accountSnapshot: 'single@example.com',
        },
      ]
    );
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    hook.unmount();
  });

  it('rejects a same-locator replacement before patching fields', async () => {
    const replacement = {
      id: 'runtime-auth-1',
      name: 'same.json',
      authIndex: 'auth-1',
      type: 'codex',
      account_id: 'replacement-account',
    } as AuthFileItem;
    mocks.list.mockResolvedValue({ files: [replacement] });
    mockAuthFileLookup([replacement]);
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [
          {
            name: 'same.json',
            runtimeId: 'runtime-auth-1',
            authIndex: 'auth-1',
            provider: 'codex',
            accountId: 'original-account',
          },
        ],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 0, failed: 1, failedNames: ['same.json'] });
    hook.unmount();
  });

  it('does not widen a shared-file selection when a target lacks auth_index', async () => {
    const files = [
      {
        id: 'runtime-auth-1',
        name: 'shared.json',
        type: 'xai',
        account: 'first@example.com',
      },
      {
        id: 'runtime-auth-2',
        name: 'shared.json',
        type: 'xai',
        account: 'second@example.com',
      },
    ] as AuthFileItem[];
    mocks.list.mockResolvedValue({ files });
    mockAuthFileLookup(files);
    const hook = mountUseAuthFilesData();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [
          {
            name: 'shared.json',
            runtimeId: 'runtime-auth-1',
            provider: 'xai',
            accountSnapshot: 'first@example.com',
          },
        ],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 0, failed: 1, failedNames: ['shared.json'] });
    hook.unmount();
  });

  it('scopes single priority mutation without full-list reads or global reloads', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, { ...targetFile, priority: 10 }]);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup.mock.calls).toEqual([
      [{ name: 'target.json' }],
      [{ name: 'runtime-target' }],
      [{ name: 'target.json' }],
    ]);
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(targetFile),
      { priority: 10 },
      [getAuthFilePatchTarget(targetFile)]
    );
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, { ...targetFile, priority: 10 }]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_fields_success',
      'success'
    );
    hook.unmount();
  });

  it('scopes single note mutation using the same optimized path', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, { ...targetFile, note: 'hello' }]);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { note: 'hello' }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup.mock.calls).toEqual([
      [{ name: 'target.json' }],
      [{ name: 'runtime-target' }],
      [{ name: 'target.json' }],
    ]);
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, { ...targetFile, note: 'hello' }]);
    hook.unmount();
  });

  it('scopes single websockets mutation and adopts authoritative read-back state', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, { ...targetFile, websockets: false }]);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { websockets: false }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, { ...targetFile, websockets: false }]);
    hook.unmount();
  });

  it('adopts server-normalized field values instead of optimistic requested values', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, { ...targetFile, priority: 5 }]);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files.find((f) => f.name === targetFile.name)?.priority).toBe(5);
    hook.unmount();
  });

  it('preserves shared source siblings and uses patchFieldsForAuthIndexes for single-target shared mutations', async () => {
    const sharedFile1: AuthFileItem = {
      name: 'shared.json',
      id: 'runtime-auth-1',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'account-1',
      priority: 1,
    };
    const sharedFile2: AuthFileItem = {
      name: 'shared.json',
      id: 'runtime-auth-2',
      auth_index: 'auth-2',
      type: 'codex',
      account_id: 'account-2',
      priority: 1,
    };
    const readBackFile1 = { ...sharedFile1, priority: 10 };
    mocks.list.mockResolvedValue({ files: [unrelatedFile, sharedFile1, sharedFile2] });
    mockAuthFileLookup([unrelatedFile, sharedFile1, sharedFile2]);
    mocks.patchFieldsForAuthIndexes.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, readBackFile1, sharedFile2]);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(sharedFile1)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).toHaveBeenCalledExactlyOnceWith(
      'shared.json',
      [getAuthFilePatchTarget(sharedFile1)],
      [getAuthFilePatchTarget(sharedFile1), getAuthFilePatchTarget(sharedFile2)],
      { priority: 10 }
    );
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, readBackFile1, sharedFile2]);
    hook.unmount();
  });

  it('rejects cross-source runtime collisions without sending field mutations', async () => {
    const collision: AuthFileItem = {
      name: 'collision.json',
      id: 'runtime-target',
      type: 'codex',
      account: 'collision@example.com',
    };
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile, collision]);
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 0, failed: 1, failedNames: [targetFile.name] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, targetFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_fields_partial',
      'warning'
    );
    hook.unmount();
  });

  it('reconciles server state after a field patch response is lost without reclassifying failure', async () => {
    const initialTarget = { ...targetFile, priority: 1 };
    const readBack = { ...targetFile, priority: 7, note: 'server metadata' };
    mocks.list.mockResolvedValue({ files: [unrelatedFile, initialTarget] });
    mockAuthFileLookup([unrelatedFile, initialTarget]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mockAuthFileLookup([unrelatedFile, readBack]);
      throw new Error('response lost');
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(initialTarget)],
        { priority: 7 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledExactlyOnceWith(
      getAuthFilePatchTarget(initialTarget),
      { priority: 7 },
      [getAuthFilePatchTarget(initialTarget)]
    );
    expect(result).toEqual({ success: 0, failed: 1, failedNames: [initialTarget.name] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, readBack]);
    expect(mocks.showNotification).toHaveBeenCalledExactlyOnceWith(
      'auth_files.batch_fields_partial',
      'warning'
    );
    expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
    hook.unmount();
  });

  it('preserves local state and warns when field patch is rejected and read-back fails', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mocks.lookup.mockRejectedValue(new Error('source read-back failed'));
      throw new Error('patch rejected');
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 0, failed: 1, failedNames: [targetFile.name] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, targetFile]);
    expect(mocks.showNotification.mock.calls).toEqual([
      ['auth_files.batch_fields_partial', 'warning'],
      ['notification.refresh_failed: source read-back failed', 'warning'],
    ]);
    expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
    hook.unmount();
  });

  it('retains confirmed success and warns when read-back fails after successful field patch', async () => {
    mocks.list.mockResolvedValue({ files: [unrelatedFile, targetFile] });
    mockAuthFileLookup([unrelatedFile, targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mocks.lookup.mockRejectedValue(new Error('source read-back failed'));
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 1, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([unrelatedFile, targetFile]);
    expect(mocks.showNotification.mock.calls).toEqual([
      ['auth_files.batch_fields_success', 'success'],
      ['notification.refresh_failed: source read-back failed', 'warning'],
    ]);
    expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
    hook.unmount();
  });

  it('rereads the source once instead of overwriting a newer revision with a stale read-back', async () => {
    const readBack = createDeferred<AuthFileItem[]>();
    const newerFile = { ...targetFile, note: 'newer edit', priority: 2 };
    mocks.list
      .mockResolvedValueOnce({ files: [targetFile] })
      .mockResolvedValueOnce({ files: [newerFile] });
    mockAuthFileLookup([targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mocks.lookup.mockReturnValueOnce(readBack.promise);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    let mutationPromise!: Promise<unknown>;
    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mockAuthFileLookup([newerFile]);

    await act(async () => {
      readBack.resolve([{ ...targetFile, priority: 10 }]);
      await mutationPromise;
    });

    expect(hook.getCurrent().files).toEqual([newerFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.batch_fields_success',
      'success'
    );
    hook.unmount();
  });

  it('keeps the newest revision and warns if the one allowed read-back retry is also stale', async () => {
    const firstRead = createDeferred<AuthFileItem[]>();
    const secondRead = createDeferred<AuthFileItem[]>();
    const newerFile = { ...targetFile, note: 'newer' };
    const latestFile = { ...targetFile, note: 'latest' };
    mocks.list
      .mockResolvedValueOnce({ files: [targetFile] })
      .mockResolvedValueOnce({ files: [newerFile] })
      .mockResolvedValueOnce({ files: [latestFile] });
    mockAuthFileLookup([targetFile]);
    mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
      mocks.lookup
        .mockReturnValueOnce(firstRead.promise)
        .mockReturnValueOnce(secondRead.promise);
      return { status: 'ok' };
    });
    const hook = mountUseAuthFilesData();
    let mutationPromise!: Promise<unknown>;
    await act(async () => {
      await hook.getCurrent().loadFiles();
      mutationPromise = hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(targetFile)],
        { priority: 10 }
      );
    });

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    await act(async () => {
      firstRead.resolve([{ ...targetFile, priority: 10 }]);
      await Promise.resolve();
    });

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    await act(async () => {
      secondRead.resolve([{ ...targetFile, priority: 10 }]);
      await mutationPromise;
    });

    expect(hook.getCurrent().files).toEqual([latestFile]);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.refresh_failed',
      'warning'
    );
    hook.unmount();
  });

  it.each([
    ['mutation-fulfilled', false],
    ['mutation-rejected', true],
    ['readback-fulfilled', false],
    ['readback-rejected', true],
  ] as const)(
    'ignores late old connection responses during %s (reject=%s)',
    async (scenario, reject) => {
      const oldDeferred = createDeferred<unknown>();
      const newPatch = createDeferred<unknown>();
      const newFile = { ...targetFile, account: 'connection-b@example.com' };
      let onNewConnection = false;
      mocks.list
        .mockResolvedValueOnce({ files: [targetFile] })
        .mockResolvedValueOnce({ files: [newFile] });
      mockAuthFileLookup([targetFile]);

      mocks.patchFieldsWithPluginSourceFallback.mockImplementation(async () => {
        if (onNewConnection) return newPatch.promise;
        if (scenario === 'mutation-fulfilled' || scenario === 'mutation-rejected') {
          return oldDeferred.promise;
        }
        mocks.lookup.mockReturnValueOnce(oldDeferred.promise as Promise<AuthFileItem[]>);
        return { status: 'ok' };
      });

      const hook = mountUseAuthFilesData('connection-a');
      let oldPromise!: ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>;
      await act(async () => {
        await hook.getCurrent().loadFiles();
        oldPromise = hook.getCurrent().batchPatchFields(
          [getAuthFilePatchTarget(targetFile)],
          { priority: 1 }
        );
      });

      hook.rerender('connection-b');
      onNewConnection = true;
      mockAuthFileLookup([newFile]);
      mocks.showNotification.mockClear();

      let newPromise!: ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>;
      await act(async () => {
        await hook.getCurrent().loadFiles();
        newPromise = hook.getCurrent().batchPatchFields(
          [getAuthFilePatchTarget(newFile)],
          { priority: 2 }
        );
      });
      expect(hook.getCurrent().batchFieldsUpdating).toBe(true);

      await act(async () => {
        if (reject) oldDeferred.reject(new Error('old connection failed'));
        else if (scenario.startsWith('readback')) oldDeferred.resolve([targetFile]);
        else oldDeferred.resolve({ status: 'ok' });
        await oldPromise;
      });

      expect(hook.getCurrent().files).toEqual([newFile]);
      expect(hook.getCurrent().batchFieldsUpdating).toBe(true);
      expect(mocks.showNotification).not.toHaveBeenCalled();

      await act(async () => {
        mockAuthFileLookup([{ ...newFile, priority: 2 }]);
        newPatch.resolve({ status: 'ok' });
        await newPromise;
      });

      expect(hook.getCurrent().files).toEqual([{ ...newFile, priority: 2 }]);
      expect(hook.getCurrent().batchFieldsUpdating).toBe(false);
      expect(mocks.showNotification).toHaveBeenCalledWith(
        'auth_files.batch_fields_success',
        'success'
      );
      hook.unmount();
    }
  );

  it('keeps multi-target field mutations on full-list inventory and reloads on success', async () => {
    const file1 = { ...targetFile, id: 'runtime-1', name: 'target1.json' };
    const file2 = { ...targetFile, id: 'runtime-2', name: 'target2.json' };
    mocks.list
      .mockResolvedValueOnce({ files: [file1, file2] })
      .mockResolvedValueOnce({ files: [file1, file2] })
      .mockResolvedValueOnce({ files: [{ ...file1, priority: 10 }, { ...file2, priority: 10 }] });
    mocks.patchFieldsWithPluginSourceFallback.mockResolvedValue({ status: 'ok' });
    const hook = mountUseAuthFilesData();
    await act(async () => {
      await hook.getCurrent().loadFiles();
    });
    mocks.list.mockClear();
    mocks.lookup.mockClear();

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook.getCurrent().batchPatchFields(
        [getAuthFilePatchTarget(file1), getAuthFilePatchTarget(file2)],
        { priority: 10 }
      );
    });

    expect(mocks.lookup).not.toHaveBeenCalled();
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(result).toEqual({ success: 2, failed: 0, failedNames: [] });
    expect(hook.getCurrent().files).toEqual([
      { ...file1, priority: 10 },
      { ...file2, priority: 10 },
    ]);
    hook.unmount();
  });

  describe('useAuthFilesData reconcileAuthFileSource', () => {
    it('preserves unrelated files, replaces target source, and performs no full-list reads', async () => {
      const unrelated = { ...targetFile, id: 'runtime-unrelated', name: 'unrelated.json' };
      const oldTarget = { ...targetFile, id: 'runtime-target', name: 'target.json', note: 'old' };
      const updatedTarget = { ...oldTarget, note: 'updated' };
      mocks.list.mockResolvedValueOnce({ files: [unrelated, oldTarget] });
      mockAuthFileLookup([unrelated, updatedTarget]);
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });
      mocks.list.mockClear();
      mocks.lookup.mockClear();

      await act(async () => {
        await hook.getCurrent().reconcileAuthFileSource('target.json');
      });

      expect(mocks.list).not.toHaveBeenCalled();
      expect(mocks.lookup).toHaveBeenCalledWith({ name: 'target.json' });
      expect(hook.getCurrent().files).toEqual([unrelated, updatedTarget]);
      hook.unmount();
    });

    it('replaces all members of a shared source without updating only a single row', async () => {
      const unrelated = { ...targetFile, id: 'runtime-unrelated', name: 'unrelated.json' };
      const shared1 = {
        ...targetFile,
        id: 'runtime-shared-1',
        name: 'shared.json',
        auth_index: 'auth-1',
        note: 'old-1',
      };
      const shared2 = {
        ...targetFile,
        id: 'runtime-shared-2',
        name: 'shared.json',
        auth_index: 'auth-2',
        note: 'old-2',
      };
      const updated1 = { ...shared1, note: 'new-1' };
      const updated2 = { ...shared2, note: 'new-2' };
      mocks.list.mockResolvedValueOnce({ files: [unrelated, shared1, shared2] });
      mockAuthFileLookup([unrelated, updated1, updated2]);
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });
      mocks.list.mockClear();

      await act(async () => {
        await hook.getCurrent().reconcileAuthFileSource('shared.json');
      });

      expect(mocks.list).not.toHaveBeenCalled();
      expect(hook.getCurrent().files).toEqual([unrelated, updated1, updated2]);
      hook.unmount();
    });

    it('retries same source once and avoids overwriting newer local state when revision changes', async () => {
      const firstRead = createDeferred<AuthFileItem[]>();
      const secondRead = createDeferred<AuthFileItem[]>();
      const initialFile = { ...targetFile, name: 'target.json', note: 'initial' };
      const newerFile = { ...targetFile, name: 'target.json', note: 'newer' };
      const reconciledFile = { ...targetFile, name: 'target.json', note: 'reconciled' };
      mocks.list
        .mockResolvedValueOnce({ files: [initialFile] })
        .mockResolvedValueOnce({ files: [newerFile] });
      mocks.lookup
        .mockReturnValueOnce(firstRead.promise)
        .mockReturnValueOnce(secondRead.promise);
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      let reconcilePromise!: Promise<void>;
      act(() => {
        reconcilePromise = hook.getCurrent().reconcileAuthFileSource('target.json');
      });

      // Concurrent files reload modifies filesRevision
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      // First read returns with stale data, should trigger retry
      await act(async () => {
        firstRead.resolve([initialFile]);
        await Promise.resolve();
      });

      // Second read returns with reconciled data
      await act(async () => {
        secondRead.resolve([reconciledFile]);
        await reconcilePromise;
      });

      expect(hook.getCurrent().files).toEqual([reconciledFile]);
      expect(mocks.lookup).toHaveBeenCalledTimes(2);
      hook.unmount();
    });

    it('preserves newer local state and reports reconciliation failure when second read is still stale', async () => {
      const firstRead = createDeferred<AuthFileItem[]>();
      const secondRead = createDeferred<AuthFileItem[]>();
      const initialFile = { ...targetFile, name: 'target.json', note: 'initial' };
      const newerFile = { ...targetFile, name: 'target.json', note: 'newer' };
      const latestFile = { ...targetFile, name: 'target.json', note: 'latest' };
      mocks.list
        .mockResolvedValueOnce({ files: [initialFile] })
        .mockResolvedValueOnce({ files: [newerFile] })
        .mockResolvedValueOnce({ files: [latestFile] });
      mocks.lookup
        .mockReturnValueOnce(firstRead.promise)
        .mockReturnValueOnce(secondRead.promise);
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      let reconcilePromise!: Promise<void>;
      act(() => {
        reconcilePromise = hook.getCurrent().reconcileAuthFileSource('target.json');
      });

      // First concurrent update
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      // First read resolves
      await act(async () => {
        firstRead.resolve([{ ...initialFile, note: 'stale-1' }]);
        await Promise.resolve();
      });

      // Second concurrent update before second read finishes
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      let capturedError: unknown;
      await act(async () => {
        secondRead.resolve([{ ...initialFile, note: 'stale-2' }]);
        try {
          await reconcilePromise;
        } catch (err) {
          capturedError = err;
        }
      });

      expect(capturedError).toBeDefined();
      expect(hook.getCurrent().files).toEqual([latestFile]);
      expect(mocks.lookup).toHaveBeenCalledTimes(2);
      hook.unmount();
    });

    it('does not modify new connection files when an old connection lookup finishes late', async () => {
      const oldLookup = createDeferred<AuthFileItem[]>();
      const oldFile = { ...targetFile, name: 'target.json', account: 'old@example.com' };
      const newFile = { ...targetFile, name: 'target.json', account: 'new@example.com' };
      mocks.list
        .mockResolvedValueOnce({ files: [oldFile] })
        .mockResolvedValueOnce({ files: [newFile] });
      mocks.lookup.mockReturnValueOnce(oldLookup.promise);

      const hook = mountUseAuthFilesData('connection-a');
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      let oldReconcilePromise!: Promise<void>;
      act(() => {
        oldReconcilePromise = hook.getCurrent().reconcileAuthFileSource('target.json');
      });

      // Switch to connection-b
      hook.rerender('connection-b');
      mockAuthFileLookup([newFile]);
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });

      expect(hook.getCurrent().files).toEqual([newFile]);

      // Old connection lookup returns
      await act(async () => {
        oldLookup.resolve([{ ...oldFile, note: 'reconciled-old' }]);
        await oldReconcilePromise;
      });

      expect(hook.getCurrent().files).toEqual([newFile]);
      hook.unmount();
    });

    it('propagates lookup errors to caller without performing a full list reload', async () => {
      const initialFile = { ...targetFile, name: 'target.json' };
      mocks.list.mockResolvedValueOnce({ files: [initialFile] });
      mocks.lookup.mockRejectedValueOnce(new Error('lookup failed'));
      const hook = mountUseAuthFilesData();
      await act(async () => {
        await hook.getCurrent().loadFiles();
      });
      mocks.list.mockClear();

      let capturedError: unknown;
      await act(async () => {
        try {
          await hook.getCurrent().reconcileAuthFileSource('target.json');
        } catch (err) {
          capturedError = err;
        }
      });

      expect(capturedError).toBeInstanceOf(Error);
      expect((capturedError as Error).message).toBe('lookup failed');
      expect(mocks.list).not.toHaveBeenCalled();
      expect(hook.getCurrent().files).toEqual([initialFile]);
      hook.unmount();
    });
  });
});
