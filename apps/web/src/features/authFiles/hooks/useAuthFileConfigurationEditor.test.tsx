import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { useEffect } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  useAuthFileConfigurationEditor,
  type UseAuthFileConfigurationEditorResult,
} from './useAuthFileConfigurationEditor';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    downloadText: vi.fn(),
    list: vi.fn(),
    patchFieldsWithPluginSourceFallback: vi.fn(),
    patchFieldsForAuthIndexes: vi.fn(),
    showNotification: vi.fn(),
    loadFiles: vi.fn(async () => undefined),
    onSaved: vi.fn(),
    t: (key: string, options?: { name?: string }) =>
      options?.name ? `${key}:${options.name}` : key,
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: mocks.t,
  }),
}));

vi.mock('@/services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api')>();
  return {
    ...actual,
    authFilesApi: {
      ...actual.authFilesApi,
      downloadText: mocks.downloadText,
      list: mocks.list,
      patchFieldsWithPluginSourceFallback: mocks.patchFieldsWithPluginSourceFallback,
      patchFieldsForAuthIndexes: mocks.patchFieldsForAuthIndexes,
    },
  };
});

vi.mock('@/stores', () => ({
  useNotificationStore: (
    selector: (state: { showNotification: typeof mocks.showNotification }) => unknown
  ) => selector({ showNotification: mocks.showNotification }),
}));

const mockAuthState = vi.hoisted(() => ({ serverVersion: 'v8.0.55' as string | null }));
vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: { serverVersion: string | null }) => unknown) =>
    selector(mockAuthState),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const file = {
  name: 'xai.json',
  id: 'runtime-xai-1',
  type: 'xai',
  provider: 'xai',
  authIndex: 'auth-1',
  account: 'xai@example.com',
  account_id: 'account-1',
} as AuthFileItem;

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

const setDownloadedRecord = (record: Record<string, unknown>) => {
  mocks.downloadText.mockResolvedValue(JSON.stringify(record));
};

describe('useAuthFileConfigurationEditor', () => {
  let renderer: ReactTestRenderer | null = null;
  let latest: UseAuthFileConfigurationEditorResult | null = null;

  function Harness({
    enabled = true,
    activeFile = file,
    connectionKey = 'connection-a',
    sourceMemberCount = 1,
  }: {
    enabled?: boolean;
    activeFile?: AuthFileItem;
    connectionKey?: string;
    sourceMemberCount?: number;
  }) {
    const editor = useAuthFileConfigurationEditor({
      file: activeFile,
      enabled,
      disableControls: false,
      sourceMemberCount,
      connectionKey,
      loadFiles: mocks.loadFiles,
      onSaved: mocks.onSaved,
    });
    useEffect(() => {
      latest = editor;
    }, [editor]);
    return null;
  }

  beforeEach(() => {
    latest = null;
    renderer = null;
    mocks.downloadText.mockReset();
    mocks.list.mockReset();
    mocks.patchFieldsWithPluginSourceFallback.mockReset();
    mocks.patchFieldsForAuthIndexes.mockReset();
    mocks.showNotification.mockReset();
    mocks.loadFiles.mockReset();
    mocks.loadFiles.mockResolvedValue(undefined);
    mocks.onSaved.mockReset();
    mocks.downloadText.mockResolvedValue(
      JSON.stringify({
        type: 'xai',
        auth_index: 'auth-1',
        account_id: 'account-1',
        using_api: false,
        access_token: 'secret-token',
        note: 'old',
      })
    );
    mocks.list.mockResolvedValue({ files: [file] });
    mocks.patchFieldsWithPluginSourceFallback.mockResolvedValue({ status: 'ok' });
    mocks.patchFieldsForAuthIndexes.mockResolvedValue(undefined);
  });

  afterEach(() => {
    act(() => renderer?.unmount());
  });

  it('loads, saves a minimal identity-verified patch, and keeps the editor open', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    expect(latest?.state).toMatchObject({
      fileName: 'xai.json',
      loading: false,
      providerKey: 'xai',
    });

    act(() => latest?.updateField('note', 'updated'));
    expect(latest?.dirty).toBe(true);
    expect(latest?.rawDataText).toContain('"note": "old"');
    expect(latest?.rawDataText).not.toContain('updated');
    expect(latest?.rawDataText).toContain('"access_token": "[redacted]"');
    expect(latest?.rawDataText).not.toContain('secret-token');

    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      using_api: false,
      access_token: 'secret-token',
      note: 'updated',
    });

    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'xai.json',
        runtimeId: 'runtime-xai-1',
        authIndex: 'auth-1',
      }),
      { note: 'updated' },
      [
        expect.objectContaining({
          name: 'xai.json',
          runtimeId: 'runtime-xai-1',
          authIndex: 'auth-1',
        }),
      ]
    );
    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);
    expect(mocks.onSaved).toHaveBeenCalledWith('xai.json');
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_saved_success', 'success');
    expect(latest?.state?.record).toMatchObject({ note: 'updated' });
    expect(latest?.rawDataText).toContain('"note": "updated"');
    expect(latest?.dirty).toBe(false);
    expect(renderer).not.toBeNull();
  });

  it.each([
    [undefined, 'enabled', false],
    [true, 'inherit', null],
  ] as const)(
    'saves credential cooling %j -> %s as %j',
    async (initialOverride, nextPolicy, expectedOverride) => {
      mocks.downloadText.mockResolvedValueOnce(
        JSON.stringify({
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          ...(initialOverride === undefined ? {} : { disable_cooling: initialOverride }),
        })
      );
      await act(async () => {
        renderer = create(<Harness />);
        await Promise.resolve();
      });
      await flush();

      act(() => latest?.updateField('disableCooling', nextPolicy));
      setDownloadedRecord({
        type: 'xai',
        auth_index: 'auth-1',
        account_id: 'account-1',
        disable_cooling: expectedOverride,
      });
      await act(async () => {
        await latest?.save();
      });

      expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
        expect.anything(),
        { disable_cooling: expectedOverride },
        expect.anything()
      );
      expect(latest?.draft?.disableCooling).toBe(nextPolicy);
      expect(latest?.dirty).toBe(false);
    }
  );

  it('rewrites the verified source when a canonical exclusion must replace a legacy key', async () => {
    mocks.downloadText.mockResolvedValueOnce(
      JSON.stringify({
        type: 'xai',
        auth_index: 'auth-1',
        account_id: 'account-1',
        excluded_models: ['legacy-model'],
      })
    );
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('excludedModelsText', 'canonical-model'));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      'excluded-models': ['canonical-model'],
    });
    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(mocks.patchFieldsForAuthIndexes).toHaveBeenCalledWith(
      'xai.json',
      [
        expect.objectContaining({
          name: 'xai.json',
          runtimeId: 'runtime-xai-1',
          authIndex: 'auth-1',
        }),
      ],
      [
        expect.objectContaining({
          name: 'xai.json',
          runtimeId: 'runtime-xai-1',
          authIndex: 'auth-1',
        }),
      ],
      {
        'excluded-models': ['canonical-model'],
        excluded_models: null,
      }
    );
    expect(latest?.state?.record).toMatchObject({
      'excluded-models': ['canonical-model'],
    });
    expect(latest?.state?.record).not.toHaveProperty('excluded_models');
  });

  it.each([
    [null, false],
    ['custom', false],
    ['v8.0.32-0-gabc1234-dirty', false],
    ['v8.0.32-0-gabc1234', true],
    ['v8.0.32', true],
    ['v8.0.55', true],
  ] as const)(
    'blocks a pre-existing exclusion change when CPA version is %j (savable: %s)',
    async (serverVersion, savable) => {
      mockAuthState.serverVersion = serverVersion;
      try {
        await act(async () => {
          renderer = create(<Harness />);
          await Promise.resolve();
        });
        await flush();

        act(() => latest?.updateField('excludedModelsText', 'gpt-4o'));
        expect(latest?.dirty).toBe(true);
        expect(latest?.canSave).toBe(savable);

        await act(async () => {
          await latest?.save();
        });

        if (savable) {
          expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
            expect.anything(),
            { 'excluded-models': ['gpt-4o'] },
            expect.anything()
          );
        } else {
          expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
          expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
          expect(latest?.dirty).toBe(true);
        }
      } finally {
        mockAuthState.serverVersion = 'v8.0.55';
      }
    }
  );

  it('keeps an unrecognized CPA version from blocking unrelated configuration saves', async () => {
    mockAuthState.serverVersion = 'custom';
    try {
      await act(async () => {
        renderer = create(<Harness />);
        await Promise.resolve();
      });
      await flush();

      act(() => latest?.updateField('note', 'updated'));
      expect(latest?.canSave).toBe(true);
    } finally {
      mockAuthState.serverVersion = 'v8.0.55';
    }
  });

  it('does not mark formatting-only edits as unsaved but keeps invalid edits guarded', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', '  old  '));
    expect(latest?.dirty).toBe(false);
    expect(latest?.canSave).toBe(false);

    act(() => latest?.updateField('priority', '1.5'));
    expect(latest?.dirty).toBe(true);
    expect(latest?.canSave).toBe(false);
    expect(latest?.errors.priority).toBe('accounts.config_error_priority_integer');
  });

  it('keeps a multi-credential single-object source read-only', async () => {
    await act(async () => {
      renderer = create(<Harness sourceMemberCount={2} />);
      await Promise.resolve();
    });
    await flush();

    expect(latest?.sourceMemberCount).toBe(2);
    expect(latest?.sharedSourceReadOnly).toBe(true);
    expect(latest?.canSave).toBe(false);
    expect(latest?.rawDataText).toContain('"note": "old"');

    act(() => latest?.updateField('note', 'must-not-change'));
    expect(latest?.draft?.note).toBe('old');
    expect(latest?.dirty).toBe(false);
    await act(async () => {
      await latest?.save();
    });
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
  });

  it('keeps members of a JSON array editable even when they share a physical file', async () => {
    mocks.downloadText.mockResolvedValue(
      JSON.stringify([
        {
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'first',
        },
        {
          type: 'xai',
          auth_index: 'auth-2',
          account_id: 'account-2',
          note: 'second',
        },
      ])
    );
    await act(async () => {
      renderer = create(<Harness sourceMemberCount={2} />);
      await Promise.resolve();
    });
    await flush();

    expect(latest?.state?.recordIndex).toBe(0);
    expect(latest?.sharedSourceReadOnly).toBe(false);
    act(() => latest?.updateField('note', 'updated-first'));
    expect(latest?.dirty).toBe(true);
    expect(latest?.canSave).toBe(true);
  });

  it('fails closed when the credential identity disappears before saving', async () => {
    mocks.list.mockResolvedValue({ files: [] });
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated'));
    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );
    expect(latest?.dirty).toBe(true);
    expect(latest?.state?.saving).toBe(false);
  });

  it('deduplicates repeated save requests for the same credential', async () => {
    let resolvePatch!: () => void;
    mocks.patchFieldsWithPluginSourceFallback.mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePatch = () => resolve({ status: 'ok' });
      })
    );
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();
    act(() => latest?.updateField('note', 'updated'));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: 'updated',
    });

    let firstSave!: Promise<void>;
    let duplicateSave!: Promise<void>;
    await act(async () => {
      firstSave = latest!.save();
      duplicateSave = latest!.save();
      await Promise.resolve();
    });

    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolvePatch();
      await Promise.all([firstSave, duplicateSave]);
    });

    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_saved_success', 'success');
  });

  it('normalizes a negative weight to an explicit zero after saving', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('weight', '-8'));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      weight: 0,
    });
    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      expect.anything(),
      { weight: 0 },
      expect.anything()
    );
    expect(latest?.draft?.weight).toBe('0');
    expect(latest?.dirty).toBe(false);
  });

  it('keeps a successful save successful when the list refresh fails', async () => {
    mocks.loadFiles.mockRejectedValueOnce(new Error('refresh failed'));
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated'));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: 'updated',
    });
    await act(async () => {
      await latest?.save();
    });

    expect(latest?.dirty).toBe(false);
    expect(mocks.onSaved).toHaveBeenCalledWith('xai.json');
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_saved_success', 'success');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.load_failed'),
      'warning'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );
  });

  it('uses the persisted source record for raw data after saving', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', ''));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: '',
    });

    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      expect.anything(),
      { note: '' },
      expect.anything()
    );
    expect(latest?.state?.record).toHaveProperty('note', '');
    expect(latest?.rawDataText).toContain('"note": ""');
    expect(latest?.dirty).toBe(false);
  });

  it('does not overwrite a sibling editor when an earlier save finishes late', async () => {
    const sibling = {
      ...file,
      id: 'runtime-xai-2',
      authIndex: 'auth-2',
      account: 'sibling@example.com',
      account_id: 'account-2',
    } as AuthFileItem;
    mocks.downloadText.mockImplementation(async (name: string) =>
      JSON.stringify(
        name === sibling.name
          ? {
              type: 'xai',
              auth_index: 'auth-2',
              account_id: 'account-2',
              note: 'sibling',
            }
          : {
              type: 'xai',
              auth_index: 'auth-1',
              account_id: 'account-1',
              note: 'old',
            }
      )
    );
    sibling.name = 'xai-sibling.json';
    mocks.list.mockResolvedValue({ files: [file, sibling] });
    let resolvePatch!: () => void;
    mocks.patchFieldsWithPluginSourceFallback.mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePatch = () => resolve({ status: 'ok' });
      })
    );

    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();
    act(() => latest?.updateField('note', 'updated'));

    let saveRequest!: Promise<void>;
    await act(async () => {
      saveRequest = latest!.save();
      await Promise.resolve();
    });
    await act(async () => {
      renderer?.update(<Harness activeFile={sibling} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await flush();

    expect(latest?.state?.fileName).toBe('xai-sibling.json');
    expect(latest?.draft?.note).toBe('sibling');

    await act(async () => {
      resolvePatch();
      await saveRequest;
    });

    expect(latest?.state?.fileName).toBe('xai-sibling.json');
    expect(latest?.draft?.note).toBe('sibling');
    expect(mocks.onSaved).not.toHaveBeenCalled();
  });

  it('does not overwrite a reloaded editor when the same credential is revisited', async () => {
    const sibling = {
      ...file,
      name: 'xai-sibling.json',
      id: 'runtime-xai-2',
      authIndex: 'auth-2',
      account: 'sibling@example.com',
      account_id: 'account-2',
    } as AuthFileItem;
    let primaryDownloadCount = 0;
    mocks.downloadText.mockImplementation(async (name: string) => {
      if (name === sibling.name) {
        return JSON.stringify({
          type: 'xai',
          auth_index: 'auth-2',
          account_id: 'account-2',
          note: 'sibling',
        });
      }
      primaryDownloadCount += 1;
      return JSON.stringify({
        type: 'xai',
        auth_index: 'auth-1',
        account_id: 'account-1',
        note: primaryDownloadCount === 1 ? 'old' : 'reloaded',
      });
    });
    mocks.list.mockResolvedValue({ files: [file, sibling] });
    let resolvePatch!: () => void;
    mocks.patchFieldsWithPluginSourceFallback.mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePatch = () => resolve({ status: 'ok' });
      })
    );

    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();
    act(() => latest?.updateField('note', 'saved-late'));

    let saveRequest!: Promise<void>;
    await act(async () => {
      saveRequest = latest!.save();
      await Promise.resolve();
    });
    await act(async () => {
      renderer?.update(<Harness activeFile={sibling} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await flush();
    await act(async () => {
      renderer?.update(<Harness activeFile={file} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await flush();

    expect(latest?.draft?.note).toBe('reloaded');
    act(() => latest?.updateField('note', 'newer-draft'));
    await act(async () => {
      await latest?.save();
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolvePatch();
      await saveRequest;
    });

    expect(latest?.draft?.note).toBe('newer-draft');
    expect(latest?.rawDataText).toContain('"note": "reloaded"');
    expect(latest?.dirty).toBe(true);
    expect(mocks.onSaved).not.toHaveBeenCalled();
  });

  it('does not expose or save a draft from a previous CPA connection', async () => {
    let resolveSecondDownload!: (value: string) => void;
    const secondDownload = new Promise<string>((resolve) => {
      resolveSecondDownload = resolve;
    });
    mocks.downloadText
      .mockResolvedValueOnce(
        JSON.stringify({
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'connection-a',
        })
      )
      .mockReturnValueOnce(secondDownload);

    await act(async () => {
      renderer = create(<Harness connectionKey="connection-a" />);
      await Promise.resolve();
    });
    await flush();
    act(() => latest?.updateField('note', 'dirty-on-a'));
    expect(latest?.dirty).toBe(true);

    await act(async () => {
      renderer?.update(<Harness connectionKey="connection-b" />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(latest?.state).toMatchObject({ loading: true, record: null, draft: null });
    expect(latest?.dirty).toBe(false);
    await act(async () => {
      await latest?.save();
    });
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();

    await act(async () => {
      resolveSecondDownload(
        JSON.stringify({
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'connection-b',
        })
      );
      await secondDownload;
    });
    await flush();

    expect(latest?.draft?.note).toBe('connection-b');
    expect(latest?.dirty).toBe(false);
  });
});
