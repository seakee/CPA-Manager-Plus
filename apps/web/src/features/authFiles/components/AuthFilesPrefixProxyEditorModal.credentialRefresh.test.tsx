import { act, type ComponentProps, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { PrefixProxyEditorState } from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import { AuthFilesPrefixProxyEditorModal } from './AuthFilesPrefixProxyEditorModal';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ children, footer }: { children?: ReactNode; footer?: ReactNode }) => (
    <div>
      <div>{children}</div>
      <div>{footer}</div>
    </div>
  ),
}));

const codexFile: AuthFileItem = {
  id: 'codex-runtime-auth-id',
  name: 'shared-codex.json',
  type: 'codex',
  authIndex: 'auth-2',
};

const createEditor = (
  authFile: AuthFileItem = codexFile,
  providerKey = 'codex'
): PrefixProxyEditorState => ({
  authFile,
  fileName: authFile.name,
  fileInfoText: JSON.stringify(authFile),
  loading: false,
  saving: false,
  error: null,
  originalText: '{}',
  rawText: '{}',
  invalidContentPreview: '',
  json: {},
  providerKey,
  prefix: '',
  proxyUrl: '',
  priority: '',
  websockets: false,
  websocketsTouched: false,
  usingApi: false,
  usingApiTouched: false,
  note: '',
  noteTouched: false,
  headersText: '',
  headersTouched: false,
  headersError: null,
});

const renderModal = (
  overrides: Partial<ComponentProps<typeof AuthFilesPrefixProxyEditorModal>> = {}
) => {
  const onRefreshCredential = vi.fn();
  let renderer!: ReactTestRenderer;

  act(() => {
    renderer = create(
      <AuthFilesPrefixProxyEditorModal
        disableControls={false}
        editor={createEditor()}
        updatedText="{}"
        dirty={false}
        credentialRefreshing={false}
        onClose={vi.fn()}
        onCopyText={vi.fn()}
        onSave={vi.fn()}
        onRefreshCredential={onRefreshCredential}
        onChange={vi.fn()}
        {...overrides}
      />
    );
  });

  return { renderer, onRefreshCredential };
};

const findCredentialRefreshButtons = (renderer: ReactTestRenderer) =>
  renderer.root
    .findAllByType('button')
    .filter((button) => button.props['aria-label'] === 'auth_files.credential_refresh_button');

describe('AuthFilesPrefixProxyEditorModal credential refresh action', () => {
  it('refreshes the exact Codex runtime auth row from the modal footer', () => {
    const { renderer, onRefreshCredential } = renderModal();
    const button = findCredentialRefreshButtons(renderer)[0];

    expect(button).toBeDefined();
    expect(button.props.title).toBe('auth_files.credential_refresh_hint');
    act(() => button.props.onClick());
    expect(onRefreshCredential).toHaveBeenCalledWith(codexFile);
  });

  it('disables the modal action while the credential refresh is pending', () => {
    const { renderer } = renderModal({ credentialRefreshing: true });
    const button = findCredentialRefreshButtons(renderer)[0];

    expect(button.props.disabled).toBe(true);
    expect(button.findAll((node) => node.props.className === 'loading-spinner')).toHaveLength(1);
  });

  it('does not show the credential refresh action for another provider', () => {
    const claudeFile: AuthFileItem = {
      name: 'claude-account.json',
      type: 'claude',
    };
    const { renderer } = renderModal({
      editor: createEditor(claudeFile, 'claude'),
    });

    expect(findCredentialRefreshButtons(renderer)).toHaveLength(0);
  });
});
