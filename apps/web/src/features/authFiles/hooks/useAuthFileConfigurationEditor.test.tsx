import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { useEffect } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  useAuthFileConfigurationEditor,
  type UseAuthFileConfigurationEditorResult,
} from './useAuthFileConfigurationEditor';

const createDeferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, resolve, reject };
};

const { mocks } = vi.hoisted(() => ({
  mocks: {
    downloadText: vi.fn(),
    list: vi.fn(),
    lookup: vi.fn(),
    patchFieldsWithPluginSourceFallback: vi.fn(),
    patchFieldsForAuthIndexes: vi.fn(),
    showNotification: vi.fn(),
    reconcileSource: vi.fn(async (_name?: string): Promise<void> => undefined),
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
      lookup: mocks.lookup,
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

  it('keeps a same-credential draft and source visible while revalidating and after failure', async () => {
    await act(async () => {
      renderer = create(<Harness />);
    });
    await flush();
    const previous = latest!.draft;
    const next = createDeferred<string>();
    mocks.downloadText.mockReturnValueOnce(next.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = latest!.reload();
    });
    expect(latest!.state?.loading).toBe(true);
    expect(latest!.draft).toEqual(previous);
    expect(latest!.rawDataText).toContain('old');
    await act(async () => {
      next.reject(new Error('offline'));
      await pending;
    });
    expect(latest!.state?.error).toBe('offline');
    expect(latest!.draft).toEqual(previous);
    expect(latest!.rawDataText).toContain('old');
    act(() => latest!.updateField('note', 'blocked draft'));
    expect(latest!.draft).toEqual(previous);
    expect(latest!.dirty).toBe(false);
    expect(latest!.canSave).toBe(false);
    await act(async () => { await latest!.reload(); });
    act(() => latest!.updateField('note', 'recovered edit'));
    expect(latest!.dirty).toBe(true);
    expect(latest!.canSave).toBe(true);
    await act(async () => { await latest!.save(); });
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      expect.anything(), { note: 'recovered edit' }, expect.anything()
    );
  });

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
      reconcileSource: mocks.reconcileSource,
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
    mocks.lookup.mockReset();
    mocks.patchFieldsWithPluginSourceFallback.mockReset();
    mocks.patchFieldsForAuthIndexes.mockReset();
    mocks.showNotification.mockReset();
    mocks.reconcileSource.mockReset();
    mocks.reconcileSource.mockResolvedValue(undefined);
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
    mocks.lookup.mockResolvedValue([file]);
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

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup).toHaveBeenCalledWith({ name: 'xai.json' });
    expect(mocks.lookup).toHaveBeenCalledWith({ name: 'runtime-xai-1' });
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
    expect(mocks.downloadText).toHaveBeenCalledWith('xai.json');
    expect(mocks.reconcileSource).toHaveBeenCalledWith('xai.json');
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
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup).toHaveBeenCalled();
    expect(mocks.reconcileSource).toHaveBeenCalledWith('xai.json');
    expect(latest?.state?.record).toMatchObject({
      'excluded-models': ['canonical-model'],
    });
    expect(latest?.state?.record).not.toHaveProperty('excluded_models');
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
    mocks.lookup.mockResolvedValue([]);
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated'));
    await act(async () => {
      await latest?.save();
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(mocks.reconcileSource).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );
    expect(latest?.dirty).toBe(true);
    expect(latest?.state?.saving).toBe(false);
  });

  it.each([
    ['runtime ID changed', [{ ...file, id: 'runtime-replacement' }] as AuthFileItem[]],
    ['account ID changed', [{ ...file, account_id: 'account-replacement' }] as AuthFileItem[]],
    [
      'cross-source runtime collision',
      [
        file,
        {
          name: 'other.json',
          id: 'runtime-xai-1',
          type: 'xai',
          provider: 'xai',
          authIndex: 'auth-2',
        } as AuthFileItem,
      ] as AuthFileItem[],
    ],
    [
      'physical selector collision',
      [
        file,
        {
          name: 'other.json',
          id: 'xai.json',
          type: 'xai',
          provider: 'xai',
        } as AuthFileItem,
      ] as AuthFileItem[],
    ],
  ])(
    'fails closed without mutation or reconcile when identity changes: %s',
    async (_label, refreshedFiles) => {
      mocks.lookup.mockResolvedValue(refreshedFiles);
      await act(async () => {
        renderer = create(<Harness />);
        await Promise.resolve();
      });
      await flush();

      act(() => latest?.updateField('note', 'updated'));
      await act(async () => {
        await latest?.save();
      });

      expect(mocks.list).not.toHaveBeenCalled();
      expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
      expect(mocks.patchFieldsForAuthIndexes).not.toHaveBeenCalled();
      expect(mocks.reconcileSource).not.toHaveBeenCalled();
      expect(mocks.showNotification).toHaveBeenCalledWith(
        expect.stringContaining('notification.update_failed'),
        'error'
      );
      expect(latest?.dirty).toBe(true);
      expect(latest?.state?.saving).toBe(false);
    }
  );

  it('fails closed when an unindexed account snapshot changes after preflight lookup', async () => {
    const unindexedFile = {
      name: 'unindexed.json',
      id: 'runtime-unindexed',
      type: 'xai',
      provider: 'xai',
      account: 'original@example.com',
    } as AuthFileItem;
    const refreshedFile = {
      ...unindexedFile,
      account: 'replacement@example.com',
    };
    mocks.downloadText.mockResolvedValue(JSON.stringify({ type: 'xai', note: 'old' }));
    mocks.lookup.mockResolvedValue([refreshedFile]);

    await act(async () => {
      renderer = create(<Harness activeFile={unindexedFile} />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated'));
    await act(async () => {
      await latest?.save();
    });

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).not.toHaveBeenCalled();
    expect(mocks.reconcileSource).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );
    expect(latest?.dirty).toBe(true);
    expect(latest?.state?.saving).toBe(false);
  });

  it('saves an editable JSON-array member with full source identities and reconciles the shared source', async () => {
    const member1 = {
      name: 'shared.json',
      id: 'runtime-1',
      type: 'xai',
      provider: 'xai',
      authIndex: 'auth-1',
      account_id: 'account-1',
    } as AuthFileItem;
    const member2 = {
      name: 'shared.json',
      id: 'runtime-2',
      type: 'xai',
      provider: 'xai',
      authIndex: 'auth-2',
      account_id: 'account-2',
    } as AuthFileItem;
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
    mocks.lookup.mockResolvedValue([member1, member2]);

    await act(async () => {
      renderer = create(<Harness activeFile={member1} sourceMemberCount={2} />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated-first'));
    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: 'updated-first',
    });

    await act(async () => {
      await latest?.save();
    });

    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'shared.json', authIndex: 'auth-1' }),
      { note: 'updated-first' },
      [
        expect.objectContaining({ name: 'shared.json', authIndex: 'auth-1' }),
        expect.objectContaining({ name: 'shared.json', authIndex: 'auth-2' }),
      ]
    );
    expect(mocks.reconcileSource).toHaveBeenCalledWith('shared.json');
    expect(mocks.list).not.toHaveBeenCalled();
    expect(latest?.dirty).toBe(false);
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

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolvePatch();
      await Promise.all([firstSave, duplicateSave]);
    });

    expect(mocks.reconcileSource).toHaveBeenCalledTimes(1);
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

  it('keeps a successful save visible but read-only until the failed source reload recovers', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated-note'));
    mocks.downloadText.mockRejectedValueOnce(new Error('raw download network error'));

    await act(async () => {
      await latest?.save();
    });

    expect(latest?.dirty).toBe(false);
    expect(latest?.draft?.note).toBe('updated-note');
    expect(latest?.state?.error).toBe('raw download network error');
    expect(latest?.canSave).toBe(false);
    act(() => latest?.updateField('note', 'blocked-unverified-edit'));
    expect(latest?.draft?.note).toBe('updated-note');
    await act(async () => {
      await latest?.save();
    });
    expect(mocks.patchFieldsWithPluginSourceFallback).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_saved_success', 'success');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.download_failed'),
      'warning'
    );
    expect(mocks.reconcileSource).toHaveBeenCalledWith('xai.json');
    expect(mocks.onSaved).toHaveBeenCalledWith('xai.json');

    setDownloadedRecord({
      type: 'xai',
      auth_index: 'auth-1',
      account_id: 'account-1',
      note: 'updated-note',
    });
    await act(async () => {
      await latest?.reload();
    });

    expect(latest?.state?.error).toBe('');
    act(() => latest?.updateField('note', 'recovered-edit'));
    expect(latest?.dirty).toBe(true);
    expect(latest?.canSave).toBe(true);
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it('keeps a successful save successful when the accounts source reconciliation fails', async () => {
    mocks.reconcileSource.mockRejectedValueOnce(new Error('reconciliation failed'));
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
    expect(latest?.state?.error).toBe('');
    act(() => latest?.updateField('note', 'second-edit-after-reconcile-warning'));
    expect(latest?.dirty).toBe(true);
    expect(latest?.canSave).toBe(true);
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it('keeps save successful with fallback draft and two warnings when both raw download and reconcile fail', async () => {
    mocks.reconcileSource.mockRejectedValueOnce(new Error('reconciliation failed'));
    await act(async () => {
      renderer = create(<Harness />);
      await Promise.resolve();
    });
    await flush();

    act(() => latest?.updateField('note', 'updated-note'));
    mocks.downloadText.mockRejectedValueOnce(new Error('raw download failed'));

    await act(async () => {
      await latest?.save();
    });

    expect(latest?.dirty).toBe(false);
    expect(latest?.draft?.note).toBe('updated-note');
    expect(latest?.state?.error).toBe('raw download failed');
    expect(latest?.canSave).toBe(false);
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_saved_success', 'success');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.download_failed'),
      'warning'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.load_failed'),
      'warning'
    );
    expect(mocks.onSaved).toHaveBeenCalledWith('xai.json');
    expect(mocks.list).not.toHaveBeenCalled();
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
    mocks.lookup.mockImplementation(async (target: { name: string }) => {
      if (target.name === sibling.name) return [sibling];
      if (target.name === file.name) return [file];
      return [];
    });
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
    mocks.lookup.mockImplementation(async (target: { name: string }) => {
      if (target.name === sibling.name) return [sibling];
      if (target.name === file.name) return [file];
      return [];
    });
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
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.lookup).toHaveBeenCalled();
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

  it.each([
    ['preflight late'],
    ['mutation late'],
    ['raw download late'],
    ['reconcile late'],
  ] as const)(
    'does not contaminate new connection when save finishes late on %s',
    async (phase) => {
      const deferredPreflight = createDeferred<AuthFileItem[]>();
      const deferredMutation = createDeferred<{ status: string }>();
      const deferredDownload = createDeferred<string>();
      const deferredReconcile = createDeferred<void>();

      mocks.downloadText.mockImplementation(async () =>
        JSON.stringify({
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'connection-a',
        })
      );

      if (phase === 'preflight late') {
        mocks.lookup.mockReturnValue(deferredPreflight.promise);
      } else {
        mocks.lookup.mockResolvedValue([file]);
      }

      if (phase === 'mutation late') {
        mocks.patchFieldsWithPluginSourceFallback.mockReturnValue(deferredMutation.promise);
      } else {
        mocks.patchFieldsWithPluginSourceFallback.mockResolvedValue({ status: 'ok' });
      }

      if (phase === 'reconcile late') {
        mocks.reconcileSource.mockReturnValue(deferredReconcile.promise);
      } else {
        mocks.reconcileSource.mockResolvedValue(undefined);
      }

      await act(async () => {
        renderer = create(<Harness connectionKey="connection-a" />);
        await Promise.resolve();
      });
      await flush();

      act(() => latest?.updateField('note', 'save-on-a'));

      if (phase === 'raw download late') {
        mocks.downloadText.mockReturnValue(deferredDownload.promise);
      }

      let savePromise!: Promise<void>;
      act(() => {
        savePromise = latest!.save();
      });

      // Switch to connection-b
      mocks.downloadText.mockImplementation(async () =>
        JSON.stringify({
          type: 'xai',
          auth_index: 'auth-1',
          account_id: 'account-1',
          note: 'connection-b',
        })
      );
      mocks.showNotification.mockClear();

      await act(async () => {
        renderer?.update(<Harness connectionKey="connection-b" />);
        await Promise.resolve();
        await Promise.resolve();
      });
      await flush();

      expect(latest?.draft?.note).toBe('connection-b');

      // Resolve old connection pending promise
      await act(async () => {
        if (phase === 'preflight late') deferredPreflight.resolve([file]);
        if (phase === 'mutation late') deferredMutation.resolve({ status: 'ok' });
        if (phase === 'raw download late') {
          deferredDownload.resolve(
            JSON.stringify({
              type: 'xai',
              auth_index: 'auth-1',
              account_id: 'account-1',
              note: 'save-on-a',
            })
          );
        }
        if (phase === 'reconcile late') deferredReconcile.resolve();
        await savePromise;
      });

      // New connection state is NOT overwritten
      expect(latest?.draft?.note).toBe('connection-b');
      expect(mocks.onSaved).not.toHaveBeenCalled();
      expect(mocks.showNotification).not.toHaveBeenCalledWith(
        'accounts.config_saved_success',
        'success'
      );
    }
  );
});
