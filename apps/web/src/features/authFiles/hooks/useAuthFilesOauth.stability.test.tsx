import { act, createElement, useEffect, useState } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { UseAuthFilesOauthResult } from './useAuthFilesOauth';

const { mocks } = vi.hoisted(() => {
  const translate = (key: string) => key;
  return {
    mocks: {
      getOauthExcludedModels: vi.fn(),
      getOauthModelAlias: vi.fn(),
      getModelDefinitions: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
      // Stable like production react-i18next `t` / store actions.
      translate,
    },
  };
});

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: mocks.translate,
  }),
  Trans: ({ children }: { children?: unknown }) => children ?? null,
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api', () => ({
  authFilesApi: {
    getOauthExcludedModels: mocks.getOauthExcludedModels,
    getOauthModelAlias: mocks.getOauthModelAlias,
    getModelDefinitions: mocks.getModelDefinitions,
  },
}));

import { useAuthFilesOauth } from './useAuthFilesOauth';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type HarnessApi = {
  getLatest: () => UseAuthFilesOauthResult;
  bumpRender: () => void;
  unmount: () => void;
};

/**
 * Mimics AuthFilesPage: call loaders once when they appear in the dependency list.
 * Unstable loader identities re-trigger this effect after every successful load.
 */
function AuthFilesInitEffectHarness({
  onResult,
  onBumpReady,
}: {
  onResult: (result: UseAuthFilesOauthResult) => void;
  onBumpReady: (bump: () => void) => void;
}) {
  const [renderTick, setRenderTick] = useState(0);
  const result = useAuthFilesOauth({ viewMode: 'list', files: [] });
  const { loadExcluded, loadModelAlias } = result;

  useEffect(() => {
    onBumpReady(() => setRenderTick((value) => value + 1));
  }, [onBumpReady]);

  useEffect(() => {
    void loadExcluded();
    void loadModelAlias();
  }, [loadExcluded, loadModelAlias]);

  // Force a harmless re-render path that must not recreate loader identities.
  void renderTick;
  onResult(result);
  return null;
}

const mountHarness = (): HarnessApi => {
  let latest: UseAuthFilesOauthResult | null = null;
  let bumpRender: (() => void) | null = null;
  let renderer: ReactTestRenderer | null = null;

  act(() => {
    renderer = create(
      createElement(AuthFilesInitEffectHarness, {
        onResult: (result) => {
          latest = result;
        },
        onBumpReady: (bump) => {
          bumpRender = bump;
        },
      })
    );
  });

  return {
    getLatest: () => {
      if (!latest) {
        throw new Error('useAuthFilesOauth harness did not mount');
      }
      return latest;
    },
    bumpRender: () => {
      if (!bumpRender) {
        throw new Error('useAuthFilesOauth harness bump is not ready');
      }
      act(() => {
        bumpRender?.();
      });
    },
    unmount: () => {
      act(() => {
        renderer?.unmount();
      });
      renderer = null;
    },
  };
};

const flushAsync = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe('useAuthFilesOauth loader reference stability', () => {
  let harness: HarnessApi | null = null;

  beforeEach(() => {
    mocks.getOauthExcludedModels.mockReset();
    mocks.getOauthModelAlias.mockReset();
    mocks.getModelDefinitions.mockReset();
    mocks.showNotification.mockReset();
    mocks.showConfirmation.mockReset();

    mocks.getOauthExcludedModels.mockResolvedValue({});
    mocks.getOauthModelAlias.mockResolvedValue({});
    mocks.getModelDefinitions.mockResolvedValue([]);
  });

  afterEach(() => {
    harness?.unmount();
    harness = null;
  });

  it('loads each OAuth config endpoint once and keeps loader identities stable across re-renders', async () => {
    harness = mountHarness();
    await flushAsync();

    expect(mocks.getOauthExcludedModels).toHaveBeenCalledTimes(1);
    expect(mocks.getOauthModelAlias).toHaveBeenCalledTimes(1);

    const afterFirstLoad = harness.getLatest();
    expect(afterFirstLoad.excludedError).toBe('ready');
    expect(afterFirstLoad.modelAliasError).toBe('ready');

    const loadExcludedBeforeRerender = afterFirstLoad.loadExcluded;
    const loadModelAliasBeforeRerender = afterFirstLoad.loadModelAlias;

    harness.bumpRender();
    await flushAsync();

    const afterRerender = harness.getLatest();
    expect(afterRerender.loadExcluded).toBe(loadExcludedBeforeRerender);
    expect(afterRerender.loadModelAlias).toBe(loadModelAliasBeforeRerender);
    expect(mocks.getOauthExcludedModels).toHaveBeenCalledTimes(1);
    expect(mocks.getOauthModelAlias).toHaveBeenCalledTimes(1);
  });
});
