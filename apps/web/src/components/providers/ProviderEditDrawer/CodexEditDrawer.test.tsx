import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';

const mocks = vi.hoisted(() => ({
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  showNotification: vi.fn(),
}));

vi.mock('@/stores', () => ({
  useConfigStore: (selector: (state: unknown) => unknown) =>
    selector({
      fetchConfig: mocks.fetchConfig,
      updateConfigValue: mocks.updateConfigValue,
      clearCache: mocks.clearCache,
    }),
  useNotificationStore: () => ({ showNotification: mocks.showNotification }),
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children: ReactNode;
    footer: ReactNode;
  }) => (open ? createElement('div', null, children, footer) : null),
}));

vi.mock('@/services/api', () => ({
  apiCallApi: { request: vi.fn() },
  getApiCallErrorMessage: vi.fn(() => ''),
  modelsApi: { fetchV1ModelsViaApiCall: vi.fn() },
  providersApi: {},
}));

import { CodexEditDrawer } from './CodexEditDrawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const findSaveButton = (root: ReactTestInstance) =>
  root
    .findAllByType('button')
    .find((button) => String(button.props.className ?? '').includes('btn-primary'));

describe('CodexEditDrawer load baseline guard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('does not reuse a stale xAI edit baseline after a later load failure', async () => {
    mocks.fetchConfig
      .mockResolvedValueOnce([
        { apiKey: 'xai-old', baseUrl: 'https://api.x.ai/v1', websockets: true },
      ])
      .mockRejectedValueOnce(new Error('load failed'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });

    expect(
      renderer!.root.findAllByType('input').some((input) => input.props.value === 'xai-old')
    ).toBe(true);

    await act(async () => {
      renderer!.update(
        <CodexEditDrawer
          open={false}
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });
    await act(async () => {
      renderer!.update(
        <CodexEditDrawer
          open
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });

    expect(renderer!.root.findAllByType('input')).toHaveLength(0);
    expect(findSaveButton(renderer!.root)?.props.disabled).toBe(true);

    act(() => renderer!.unmount());
  });
});
