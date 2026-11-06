import { act, createElement, useLayoutEffect } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';

const { mocks } = vi.hoisted(() => {
  return {
    mocks: {
      list: vi.fn(),
      downloadText: vi.fn(),
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
      if (options && typeof options.names === 'string' && Object.keys(options).length === 1) {
        return `${key}:${options.names}`;
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

const mockAuthState = vi.hoisted(() => ({ serverVersion: 'v8.0.55' as string | null }));
vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: { serverVersion: string | null }) => unknown) =>
    selector(mockAuthState),
}));

vi.mock('@/services/api', () => ({
  authFilesApi: {
    list: mocks.list,
    downloadText: mocks.downloadText,
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
  mockAuthState.serverVersion = 'v8.0.55';
  mocks.list.mockReset();
  mocks.downloadText.mockReset();
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

describe('useAuthFilesData service errors', () => {
  it('maps an unconfigured Manager response to the product error message', async () => {
    const error = Object.assign(new Error('usage service is not configured'), { status: 428 });
    mocks.list.mockRejectedValueOnce(error);
    const hook = mountUseAuthFilesData();

    await act(async () => hook.getCurrent().loadFiles());

    expect(hook.getCurrent().error).toBe('usage_service_errors.usage_service_not_configured');
    hook.unmount();
  });
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
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    const pendingList = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(pendingList.promise);
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

    expect(mocks.list).toHaveBeenCalledTimes(2);

    await act(async () => {
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.setStatus).not.toHaveBeenCalled();

    await act(async () => {
      pendingList.resolve({ files });
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
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
    });

    const pendingList = createDeferred<{ files: AuthFileItem[] }>();
    mocks.list.mockReturnValueOnce(pendingList.promise);
    let singlePromise!: Promise<void>;
    act(() => {
      singlePromise = hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(files[0])], false);
    });

    expect(mocks.list).toHaveBeenCalledTimes(2);

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

    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(hook.getCurrent().batchStatusUpdating).toBe(true);

    await act(async () => {
      pendingList.resolve({ files });
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
      .mockResolvedValueOnce({ files: [oldFile] })
      .mockResolvedValueOnce({ files: [newFile] })
      .mockResolvedValueOnce({ files: [newFile] });
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
      .mockResolvedValueOnce({ files: [oldFile] })
      .mockResolvedValueOnce({ files: [newFile] })
      .mockResolvedValueOnce({ files: [newFile] });
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
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [refreshedFile] });
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
      status.reject(new Error('status failed'));
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);
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
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [refreshedFile] });
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
      status.reject(new Error('status failed'));
      await mutationPromise;
    });
    expect(hook.getCurrent().files).toEqual([refreshedFile]);
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
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [replacementFile] });
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
      .mockResolvedValueOnce({ files: [initialFile] })
      .mockResolvedValueOnce({ files: [replacementFile] });
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
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
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
    expect(mocks.list).toHaveBeenCalledTimes(3);
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
    let verifiedSourceIdentities: unknown;
    mocks.setStatus.mockImplementation(
      async (_target: unknown, _disabled: unknown, verifyFallback?: () => Promise<void>) => {
        if (!verifyFallback) throw new Error('missing plugin source fallback verifier');
        verifiedSourceIdentities = await verifyFallback();
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
    hook.unmount();
  });

  it.each(['member-added', 'identity-changed', 'selector-collision'] as const)(
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
          : scenario === 'identity-changed'
            ? ([{ ...files[0], account: 'replacement@example.com' }] as AuthFileItem[])
            : ([
                ...files,
                {
                  id: 'shared.json',
                  name: 'other.json',
                  type: 'codex',
                  auth_index: 'auth-other',
                  account: 'other@example.com',
                  disabled: false,
                },
              ] as AuthFileItem[]);
      mocks.list
        .mockResolvedValueOnce({ files })
        .mockResolvedValueOnce({ files })
        .mockResolvedValueOnce({ files: freshFiles });
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
      expect(mocks.list).toHaveBeenCalledTimes(3);
      expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([false]);
      expect(mocks.showNotification).toHaveBeenCalledWith(
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
    mocks.list
      .mockResolvedValueOnce({ files: initialFiles })
      .mockResolvedValueOnce({ files: refreshedFiles });
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
    mocks.list
      .mockResolvedValueOnce({ files: initialFiles })
      .mockResolvedValueOnce({ files: refreshedFiles });
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
    mocks.setStatus.mockResolvedValue({ status: 'ok', disabled: true });
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
      ]
    );
    expect(hook.getCurrent().files.map((file) => file.disabled)).toEqual([true, true]);
    expect(
      getCodexInspectionOwnedDisableIdentityKeys('scope-source-status', [
        { ...files[0], disabled: true },
        { ...files[1], disabled: true },
      ])
    ).toEqual(new Set());
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
    mocks.list
      .mockResolvedValueOnce({ files: initialFiles })
      .mockResolvedValueOnce({ files: refreshedFiles });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook.getCurrent().loadFiles();
      await hook.getCurrent().batchSetStatus([getAuthFilePatchTarget(initialFiles[0])], false);
    });

    expect(mocks.setStatus).not.toHaveBeenCalled();
    expect(mocks.list).toHaveBeenCalledTimes(2);
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
    mocks.list
      .mockResolvedValueOnce({ files: initialFiles })
      .mockResolvedValueOnce({ files: refreshedFiles });
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
    mocks.list.mockResolvedValueOnce({ files: [file] }).mockResolvedValue({
      files: [refreshedFile],
    });
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
    mocks.list
      .mockResolvedValueOnce({ files: [refreshedFiles[0], file] })
      .mockResolvedValue({ files: refreshedFiles });
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
    mocks.list.mockResolvedValue({ files: [file] });
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

    expect(mocks.list).toHaveBeenCalledTimes(16);
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
    mocks.list.mockResolvedValueOnce({ files: [original] }).mockResolvedValue({
      files: [replacement],
    });
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
    mocks.list
      .mockResolvedValueOnce({ files: [file] })
      .mockResolvedValueOnce({ files: [file] })
      .mockResolvedValue({ files: [{ ...file, last_refresh: '2026-01-02T00:00:00Z' }] });
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
    mocks.list.mockResolvedValue({ files: [file] });
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
      mocks.list.mockResolvedValue({
        files: [{ ...file, last_refresh: '2026-01-02T00:00:00Z' }],
      });
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
    mocks.list.mockResolvedValue({ files: [file] });
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
    mocks.list.mockResolvedValue({
      files: [{ ...original, account_id: 'replacement-account' }],
    });
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
});

describe('useAuthFilesData batchPatchFields', () => {
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
    mocks.list.mockResolvedValue({
      files: [
        {
          id: 'runtime-auth-1',
          name: 'same.json',
          authIndex: 'auth-1',
          type: 'codex',
          account_id: 'replacement-account',
        },
      ],
    });
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

    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(result).toEqual({ success: 0, failed: 1, failedNames: ['shared.json'] });
    hook.unmount();
  });
});

describe('useAuthFilesData raw credential import exclusions', () => {
  const existingCredential = {
    type: 'codex',
    email: 'user@example.com',
    access_token: 'existing-access-token',
    'excluded-models': ['gpt-4o'],
  };

  const mountWithExistingCredential = (
    serverVersion: string | null,
    fileNames: string[] = ['custom-auth.json']
  ) => {
    mockAuthState.serverVersion = serverVersion;
    mocks.list.mockResolvedValue({ files: fileNames.map((name) => ({ name, type: 'codex' })) });
    mocks.downloadText.mockResolvedValue(JSON.stringify(existingCredential));
    return mountUseAuthFilesData();
  };

  const mockMissingAuthFile = () => {
    mocks.downloadText.mockRejectedValue(Object.assign(new Error('Not Found'), { status: 404 }));
  };

  // Two physical credentials that differ only by case, each with its own exclusions.
  const caseCollidingExclusions = (name: string) =>
    name === 'Alpha.json' ? ['gpt-4o'] : ['gpt-5'];

  const caseCollidingStoredCredential = (name: string) =>
    JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      access_token: `access-token-${name}`,
      'excluded-models': caseCollidingExclusions(name),
    });

  const mountWithCaseCollidingCredentials = (
    serverVersion: string | null,
    listedNames: string[]
  ) => {
    mockAuthState.serverVersion = serverVersion;
    mocks.list.mockResolvedValue({
      files: listedNames.map((name) => ({ name, type: 'codex' })),
    });
    mocks.downloadText.mockImplementation(async (name: string) => {
      if (name === 'Alpha.json' || name === 'alpha.json') {
        return caseCollidingStoredCredential(name);
      }
      throw Object.assign(new Error('Not Found'), { status: 404 });
    });
    return mountUseAuthFilesData();
  };

  const uploadCredentialFile = async (
    hook: UseAuthFilesDataHarness,
    record: unknown,
    fileName = 'custom-auth.json'
  ) => {
    const file = new File([JSON.stringify(record)], fileName, { type: 'application/json' });
    const target = { files: [file] as unknown as FileList, value: fileName };
    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });
    return target;
  };

  it.each([
    [null, 'excluded-models'],
    ['custom', 'excluded-models'],
    [null, 'excluded_models'],
    ['custom', 'excluded_models'],
  ] as const)(
    'on CPA version %j refuses pasted CPA JSON that changes %s exclusions',
    async (serverVersion, exclusionField) => {
      const hook = mountWithExistingCredential(serverVersion);
      const incoming = {
        type: 'codex',
        email: 'user@example.com',
        access_token: 'next-access-token',
        [exclusionField]: ['gpt-5'],
      };

      await expect(
        hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
      ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

      expect(mocks.saveJsonObject).not.toHaveBeenCalled();
      hook.unmount();
    }
  );

  it('still refuses an exclusion change when the file list is unavailable', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockRejectedValue(new Error('list failed'));
    mocks.downloadText.mockResolvedValue(JSON.stringify(existingCredential));
    const hook = mountUseAuthFilesData();
    const incoming = { ...existingCredential, 'excluded-models': ['gpt-5'] };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it.each([null, 'custom'] as const)(
    'allows a same-name paste that only changes other fields on CPA version %j',
    async (serverVersion) => {
      const hook = mountWithExistingCredential(serverVersion);
      const incoming = { ...existingCredential, note: 'updated' };

      await act(async () => {
        await hook
          .getCurrent()
          .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming));
      });

      expect(mocks.saveJsonObject).toHaveBeenCalledWith('custom-auth.json', incoming);
      hook.unmount();
    }
  );

  it('allows a new credential paste on an unrecognized CPA version', async () => {
    const hook = mountWithExistingCredential(null, []);
    mockMissingAuthFile();
    const incoming = {
      type: 'codex',
      email: 'new@example.com',
      access_token: 'new-access-token',
      'excluded-models': ['gpt-5'],
    };

    await act(async () => {
      await hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming));
    });

    expect(mocks.saveJsonObject).toHaveBeenCalledWith('custom-auth.json', incoming);
    hook.unmount();
  });

  it.each(['v8.0.32', 'v8.0.55'] as const)(
    'allows changed exclusions through pasted CPA JSON on CPA %s',
    async (serverVersion) => {
      const hook = mountWithExistingCredential(serverVersion);
      const incoming = { ...existingCredential, 'excluded-models': ['gpt-5'] };

      await act(async () => {
        await hook
          .getCurrent()
          .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming));
      });

      expect(mocks.saveJsonObject).toHaveBeenCalledWith('custom-auth.json', incoming);
      hook.unmount();
    }
  );

  it.each([null, 'custom'] as const)(
    'refuses a same-name file upload that changes exclusions on CPA version %j',
    async (serverVersion) => {
      const hook = mountWithExistingCredential(serverVersion);

      const target = await uploadCredentialFile(hook, {
        type: 'codex',
        email: 'user@example.com',
        'excluded-models': ['gpt-5'],
      });

      expect(mocks.uploadFiles).not.toHaveBeenCalled();
      expect(mocks.showNotification).toHaveBeenCalledWith(
        'notification.upload_failed: auth_files.upload_error_exclusions_locked:custom-auth.json',
        'error'
      );
      expect(target.value).toBe('');
      hook.unmount();
    }
  );

  it('allows a same-name upload that keeps exclusions on an unrecognized CPA version', async () => {
    const hook = mountWithExistingCredential(null);

    await uploadCredentialFile(hook, { ...existingCredential, note: 'updated' });

    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('allows a new credential upload on an unrecognized CPA version', async () => {
    const hook = mountWithExistingCredential(null, []);
    mockMissingAuthFile();

    await uploadCredentialFile(hook, {
      type: 'codex',
      email: 'new@example.com',
      access_token: 'new-access-token',
      'excluded-models': ['gpt-5'],
    });

    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it.each(['v8.0.32', 'v8.0.55'] as const)(
    'allows a same-name upload that changes exclusions on CPA %s',
    async (serverVersion) => {
      const hook = mountWithExistingCredential(serverVersion);

      await uploadCredentialFile(hook, { ...existingCredential, 'excluded-models': ['gpt-5'] });

      expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
      hook.unmount();
    }
  );

  it('refuses an overwrite when the list and the download both fail', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockRejectedValue(new Error('list failed'));
    mocks.downloadText.mockRejectedValue(new Error('download failed'));
    const hook = mountUseAuthFilesData();

    await expect(
      hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(existingCredential))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses an overwrite when the list fails and the stored credential cannot be parsed', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockRejectedValue(new Error('list failed'));
    mocks.downloadText.mockResolvedValue('not-json');
    const hook = mountUseAuthFilesData();

    await expect(
      hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(existingCredential))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses when a legacy alias becomes effective because the canonical key is gone', async () => {
    // CPA ignores `excludedModels`, so the stored file has no effective exclusions while the
    // incoming file has one.
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'custom-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({ type: 'codex', email: 'user@example.com', excludedModels: ['gpt-4o'] })
    );
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'next-access-token',
      'excluded-models': ['gpt-4o'],
    };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses when a null canonical key stops shadowing the legacy alias', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'custom-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({
        type: 'codex',
        email: 'user@example.com',
        excluded_models: null,
        'excluded-models': ['gpt-4o'],
      })
    );
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'next-access-token',
      'excluded-models': ['gpt-4o'],
    };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses an overwrite that swaps a non-string exclusion element for its text', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'custom-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({ type: 'codex', email: 'user@example.com', excluded_models: ['gpt-4o', 5] })
    );
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'next-access-token',
      excluded_models: ['gpt-4o', '5'],
    };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses an overwrite whose stored exclusion list has a non-string element', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'custom-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({ type: 'codex', email: 'user@example.com', excluded_models: [5] })
    );
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'next-access-token',
      excluded_models: [],
    };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses an overwrite whose canonical exclusion key is not a list', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'custom-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({
        type: 'codex',
        email: 'user@example.com',
        access_token: 'existing-access-token',
      })
    );
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'user@example.com',
      access_token: 'next-access-token',
      excluded_models: 'gpt-4o',
    };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('refuses an array credential whose records swap exclusions across auth indexes', async () => {
    const stored = [
      { type: 'codex', auth_index: 1, email: 'a@example.com', 'excluded-models': ['gpt-4o'] },
      { type: 'codex', auth_index: 2, email: 'b@example.com', 'excluded-models': ['gpt-5'] },
    ];
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'bulk-auth.json', type: 'codex' }] });
    mocks.downloadText.mockResolvedValue(JSON.stringify(stored));
    const hook = mountUseAuthFilesData();

    await uploadCredentialFile(
      hook,
      [
        { ...stored[0], 'excluded-models': ['gpt-5'] },
        { ...stored[1], 'excluded-models': ['gpt-4o'] },
      ],
      'bulk-auth.json'
    );

    expect(mocks.uploadFiles).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.upload_failed: auth_files.upload_error_exclusions_locked:bulk-auth.json',
      'error'
    );
    hook.unmount();
  });

  it('refuses a relative path that would overwrite a stored credential', async () => {
    const hook = mountWithExistingCredential(null);

    await expect(
      hook
        .getCurrent()
        .savePastedAuthJson(
          'cpa',
          './custom-auth.json',
          JSON.stringify({ ...existingCredential, 'excluded-models': ['gpt-5'] })
        )
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:./custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('allows a same-name paste that only adds an identity field', async () => {
    const hook = mountWithExistingCredential(null);

    await act(async () => {
      await hook
        .getCurrent()
        .savePastedAuthJson(
          'cpa',
          'custom-auth.json',
          JSON.stringify({ ...existingCredential, id: 'added-id' })
        );
    });

    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('consults a listed case variant when the exact name is absent', async () => {
    // A case-sensitive auth directory holds only `Custom-Auth.json`, so the exact probe 404s.
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [{ name: 'Custom-Auth.json', type: 'codex' }] });
    mocks.downloadText.mockImplementation(async (name: string) => {
      if (name === 'Custom-Auth.json') return JSON.stringify(existingCredential);
      throw Object.assign(new Error('Not Found'), { status: 404 });
    });
    const hook = mountUseAuthFilesData();

    await act(async () => {
      await hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(existingCredential));
    });

    expect(mocks.downloadText).toHaveBeenCalledWith('Custom-Auth.json');
    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  // A case-sensitive auth directory can hold both `Alpha.json` and `alpha.json`; the overwrite
  // has to be validated against the physical file the upload will actually rewrite.
  const caseCollisionOverwrites = [
    {
      listed: ['Alpha.json', 'alpha.json'],
      target: 'Alpha.json',
      sibling: 'alpha.json',
      incomingExclusions: ['gpt-5'],
    },
    {
      listed: ['alpha.json', 'Alpha.json'],
      target: 'alpha.json',
      sibling: 'Alpha.json',
      incomingExclusions: ['gpt-4o'],
    },
  ];

  it.each(caseCollisionOverwrites)(
    'refuses a pasted overwrite of $target that matches only the $sibling credential',
    async ({ listed, target, incomingExclusions }) => {
      const hook = mountWithCaseCollidingCredentials(null, listed);

      await expect(
        hook.getCurrent().savePastedAuthJson(
          'cpa',
          target,
          JSON.stringify({
            type: 'codex',
            email: 'user@example.com',
            access_token: 'next-access-token',
            'excluded-models': incomingExclusions,
          })
        )
      ).rejects.toThrow(`auth_files.paste_error_exclusions_locked:${target}`);

      // The exact name has to be probed first; the sibling must not answer for it.
      expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([target]);
      expect(mocks.saveJsonObject).not.toHaveBeenCalled();
      hook.unmount();
    }
  );

  it.each(caseCollisionOverwrites)(
    'refuses an uploaded overwrite of $target that matches only the $sibling credential',
    async ({ listed, target, incomingExclusions }) => {
      const hook = mountWithCaseCollidingCredentials('custom', listed);

      await uploadCredentialFile(
        hook,
        {
          type: 'codex',
          email: 'user@example.com',
          access_token: 'next-access-token',
          'excluded-models': incomingExclusions,
        },
        target
      );

      expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([target]);
      expect(mocks.uploadFiles).not.toHaveBeenCalled();
      expect(mocks.showNotification).toHaveBeenCalledWith(
        `notification.upload_failed: auth_files.upload_error_exclusions_locked:${target}`,
        'error'
      );
      hook.unmount();
    }
  );

  // Editing a field other than the model exclusions must stay possible for both physical files.
  const caseCollisionTargets = [
    { listed: ['Alpha.json', 'alpha.json'], target: 'Alpha.json' },
    { listed: ['alpha.json', 'Alpha.json'], target: 'alpha.json' },
  ];
  const caseCollisionTargetCases = caseCollisionTargets.flatMap((scenario) =>
    ([null, 'custom'] as const).map((serverVersion) => ({ ...scenario, serverVersion }))
  );

  it.each(caseCollisionTargetCases)(
    'allows a note-only paste on CPA version $serverVersion for $target',
    async ({ listed, target, serverVersion }) => {
      const hook = mountWithCaseCollidingCredentials(serverVersion, listed);
      const incoming = {
        type: 'codex',
        email: 'user@example.com',
        access_token: 'next-access-token',
        'excluded-models': caseCollidingExclusions(target),
        note: 'updated',
      };

      await act(async () => {
        await hook.getCurrent().savePastedAuthJson('cpa', target, JSON.stringify(incoming));
      });

      expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
      expect(mocks.saveJsonObject).toHaveBeenCalledWith(target, incoming);
      // The exact target settles the decision; its case sibling is not a gate.
      expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([target]);
      hook.unmount();
    }
  );

  it.each(caseCollisionTargetCases)(
    'allows a note-only upload on CPA version $serverVersion for $target',
    async ({ listed, target, serverVersion }) => {
      const hook = mountWithCaseCollidingCredentials(serverVersion, listed);

      await uploadCredentialFile(
        hook,
        {
          type: 'codex',
          email: 'user@example.com',
          access_token: 'next-access-token',
          'excluded-models': caseCollidingExclusions(target),
          note: 'updated',
        },
        target
      );

      expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
      expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([target]);
      hook.unmount();
    }
  );

  it('allows a two-file note-only batch of case-colliding credentials', async () => {
    const hook = mountWithCaseCollidingCredentials(null, ['Alpha.json', 'alpha.json']);

    const batch = ['Alpha.json', 'alpha.json'].map(
      (name) =>
        new File(
          [
            JSON.stringify({
              type: 'codex',
              email: 'user@example.com',
              access_token: `next-${name}`,
              'excluded-models': caseCollidingExclusions(name),
              note: 'updated',
            }),
          ],
          name,
          { type: 'application/json' }
        )
    );
    const target = { files: batch as unknown as FileList, value: '' };
    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });

    expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([
      'Alpha.json',
      'alpha.json',
    ]);
    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('probes both case-colliding credentials in one batch instead of collapsing them', async () => {
    const identical = JSON.stringify({
      type: 'codex',
      email: 'user@example.com',
      'excluded-models': ['gpt-5'],
    });
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({
      files: [
        { name: 'Alpha.json', type: 'codex' },
        { name: 'alpha.json', type: 'codex' },
      ],
    });
    mocks.downloadText.mockImplementation(async () => identical);
    const hook = mountUseAuthFilesData();

    const batch = ['Alpha.json', 'alpha.json'].map(
      (name) => new File([identical], name, { type: 'application/json' })
    );
    const target = { files: batch as unknown as FileList, value: '' };
    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });

    expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([
      'Alpha.json',
      'alpha.json',
    ]);
    expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('refuses a batch that leaves two case-colliding credentials with different exclusions', async () => {
    // Neither name is listed or stored yet, so only the shared folded name can reveal that the
    // two entries would land on one file on a case-insensitive backend.
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    mocks.downloadText.mockRejectedValue(Object.assign(new Error('Not Found'), { status: 404 }));
    const hook = mountUseAuthFilesData();

    const batch = [
      new File(
        [
          JSON.stringify({
            type: 'codex',
            email: 'user@example.com',
            'excluded-models': ['gpt-4o'],
          }),
        ],
        'Alpha.json',
        { type: 'application/json' }
      ),
      new File(
        [
          JSON.stringify({
            type: 'codex',
            email: 'user@example.com',
            'excluded-models': ['gpt-5'],
          }),
        ],
        'alpha.json',
        { type: 'application/json' }
      ),
    ];
    const target = { files: batch as unknown as FileList, value: '' };
    await act(async () => {
      await hook
        .getCurrent()
        .handleFileChange({ target } as unknown as Parameters<
          ReturnType<typeof useAuthFilesData>['handleFileChange']
        >[0]);
    });

    expect(mocks.downloadText.mock.calls.map(([name]) => name)).toEqual([
      'Alpha.json',
      'alpha.json',
    ]);
    expect(mocks.uploadFiles).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.upload_failed: auth_files.upload_error_exclusions_locked:alpha.json',
      'error'
    );
    hook.unmount();
  });

  it.each(['v8.0.32', 'v8.0.55'] as const)(
    'still accepts a case-colliding overwrite on CPA %s',
    async (serverVersion) => {
      const hook = mountWithCaseCollidingCredentials(serverVersion, ['Alpha.json', 'alpha.json']);

      await act(async () => {
        await hook.getCurrent().savePastedAuthJson(
          'cpa',
          'Alpha.json',
          JSON.stringify({
            type: 'codex',
            email: 'user@example.com',
            access_token: 'next-access-token',
            'excluded-models': ['gpt-5'],
          })
        );
      });

      expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
      hook.unmount();
    }
  );

  it('refuses a batch exclusion patch on an unrecognized CPA version', async () => {
    const hook = mountWithExistingCredential(null);

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook
        .getCurrent()
        .batchPatchFields(
          [{ name: 'custom-auth.json', provider: 'codex', accountSnapshot: 'user@example.com' }],
          { 'excluded-models': ['gpt-5'] }
        );
    });

    expect(result).toBeNull();
    expect(mocks.patchFields).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_files.patch_error_exclusions_locked',
      'error'
    );
    hook.unmount();
  });

  it('still forwards a batch exclusion patch on a recognized CPA version', async () => {
    const hook = mountWithExistingCredential('v8.0.55');

    let result: Awaited<ReturnType<ReturnType<typeof useAuthFilesData>['batchPatchFields']>> = null;
    await act(async () => {
      result = await hook
        .getCurrent()
        .batchPatchFields(
          [{ name: 'custom-auth.json', provider: 'codex', accountSnapshot: 'user@example.com' }],
          { 'excluded-models': ['gpt-5'] }
        );
    });

    expect(result).not.toBeNull();
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_files.patch_error_exclusions_locked',
      'error'
    );
    hook.unmount();
  });

  it('refuses when the name is absent from the listing but exists on disk', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    mocks.downloadText.mockResolvedValue(JSON.stringify(existingCredential));
    const hook = mountUseAuthFilesData();
    const incoming = { ...existingCredential, 'excluded-models': ['gpt-5'] };

    await expect(
      hook.getCurrent().savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(incoming))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:custom-auth.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('allows a new name only after the probe returns a definite 404', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    mockMissingAuthFile();
    const hook = mountUseAuthFilesData();
    const incoming = {
      type: 'codex',
      email: 'new@example.com',
      access_token: 'new-access-token',
      'excluded-models': ['gpt-5'],
    };

    await act(async () => {
      await hook.getCurrent().savePastedAuthJson('cpa', 'brand-new.json', JSON.stringify(incoming));
    });

    expect(mocks.downloadText).toHaveBeenCalledWith('brand-new.json');
    expect(mocks.saveJsonObject).toHaveBeenCalledTimes(1);
    hook.unmount();
  });

  it('fails closed when the probe of an unlisted name returns a server error', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    mocks.downloadText.mockRejectedValue(Object.assign(new Error('boom'), { status: 500 }));
    const hook = mountUseAuthFilesData();

    await expect(
      hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'brand-new.json', JSON.stringify(existingCredential))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:brand-new.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('fails closed when the probe of an unlisted name fails without a status', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    mocks.downloadText.mockRejectedValue(new Error('timeout'));
    const hook = mountUseAuthFilesData();

    await expect(
      hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'brand-new.json', JSON.stringify(existingCredential))
    ).rejects.toThrow('auth_files.paste_error_exclusions_locked:brand-new.json');

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('aborts a paste when the connection switches while the guard is probing', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    let rejectProbe: (error: unknown) => void = () => {};
    mocks.downloadText.mockImplementation(
      () =>
        new Promise<string>((_resolve, reject) => {
          rejectProbe = reject;
        })
    );
    const hook = mountUseAuthFilesData('connection-a');

    let pastePromise: Promise<string[]> | null = null;
    await act(async () => {
      pastePromise = hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(existingCredential));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    // Flush the connection switch on its own so the guard resumes only afterwards.
    act(() => {
      hook.rerender('connection-b');
    });

    await act(async () => {
      rejectProbe(Object.assign(new Error('Not Found'), { status: 404 }));
      await pastePromise;
    });

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('aborts an upload when the connection switches while the guard is probing', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    let rejectProbe: (error: unknown) => void = () => {};
    mocks.downloadText.mockImplementation(
      () =>
        new Promise<string>((_resolve, reject) => {
          rejectProbe = reject;
        })
    );
    const hook = mountUseAuthFilesData('connection-a');
    const file = new File([JSON.stringify(existingCredential)], 'fresh-auth.json', {
      type: 'application/json',
    });

    let uploadPromise: Promise<void> | null = null;
    await act(async () => {
      uploadPromise = hook.getCurrent().handleFileChange({
        target: { files: [file] as unknown as FileList, value: 'fresh-auth.json' },
      } as unknown as Parameters<ReturnType<typeof useAuthFilesData>['handleFileChange']>[0]);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    // Flush the connection switch on its own so the guard resumes only afterwards.
    act(() => {
      hook.rerender('connection-b');
    });

    await act(async () => {
      rejectProbe(Object.assign(new Error('Not Found'), { status: 404 }));
      await uploadPromise;
    });

    expect(mocks.uploadFiles).not.toHaveBeenCalled();
    hook.unmount();
  });

  it('aborts a paste when the view unmounts while the guard is probing', async () => {
    mockAuthState.serverVersion = null;
    mocks.list.mockResolvedValue({ files: [] });
    let rejectProbe: (error: unknown) => void = () => {};
    mocks.downloadText.mockImplementation(
      () =>
        new Promise<string>((_resolve, reject) => {
          rejectProbe = reject;
        })
    );
    const hook = mountUseAuthFilesData('connection-a');

    let pastePromise: Promise<string[]> | null = null;
    await act(async () => {
      pastePromise = hook
        .getCurrent()
        .savePastedAuthJson('cpa', 'custom-auth.json', JSON.stringify(existingCredential));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    hook.unmount();
    await act(async () => {
      rejectProbe(Object.assign(new Error('Not Found'), { status: 404 }));
      await expect(pastePromise).resolves.toEqual([]);
    });

    expect(mocks.saveJsonObject).not.toHaveBeenCalled();
  });
});
