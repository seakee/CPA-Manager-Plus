import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Input } from '@/components/ui/Input';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { UseAuthFileConfigurationEditorResult } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import type { AuthFileConfigurationDraft } from '@/features/authFiles/model/authFileConfiguration';
import { AccountConfigurationTab } from './AccountConfigurationTab';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
    }),
  };
});

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object' && 'children' in value) {
    return readText((value as { children?: unknown }).children);
  }
  return '';
};

const findLoadingSpinners = (renderer: ReactTestRenderer) =>
  renderer.root.findAll(
    (node) =>
      typeof node.props.className === 'string' &&
      node.props.className.split(/\s+/).includes('loading-spinner')
  );

const makeDraft = (
  overrides: Partial<AuthFileConfigurationDraft> = {}
): AuthFileConfigurationDraft => ({
  prefix: '',
  proxyUrl: '',
  priority: '',
  weight: '',
  note: '',
  headersText: '',
  excludedModelsText: '',
  disableCooling: 'inherit',
  requestRetry: '',
  websockets: false,
  xaiRoutingMode: 'grok-build',
  baseUrl: '',
  cloakMode: '',
  cloakStrictMode: false,
  cloakSensitiveWordsText: '',
  cloakCacheUserId: false,
  toolPrefixDisabled: false,
  ...overrides,
});

const makeRow = (provider = 'codex', overrides: Partial<AccountRow> = {}): AccountRow =>
  ({
    selectionKey: `credential.json\u0000${provider}-1`,
    fileName: 'credential.json',
    accountLabel: 'account@example.com',
    provider,
    planType: null,
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: `${provider}-1`,
    projectId: '',
    priority: null,
    createdAtMs: null,
    updatedAtMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    raw: { name: 'credential.json', type: provider, provider },
    ...overrides,
  }) as AccountRow;

const makeEditor = (
  provider: string,
  draft = makeDraft()
): UseAuthFileConfigurationEditorResult => ({
  state: {
    authFile: { name: 'credential.json', type: provider },
    fileName: 'credential.json',
    loading: false,
    saving: false,
    error: '',
    record: { type: provider },
    recordIndex: null,
    providerKey: provider,
    originalDraft: draft,
    draft,
  },
  draft,
  errors: {},
  dirty: false,
  canSave: false,
  rawDataText: '{}',
  sourceMemberCount: 1,
  sharedSourceReadOnly: false,
  updateField: vi.fn(),
  reset: vi.fn(),
  reload: vi.fn(async () => undefined),
  save: vi.fn(async () => undefined),
});

const renderTab = (
  row: AccountRow,
  editor: UseAuthFileConfigurationEditorResult,
  onCopyText = vi.fn()
): ReactTestRenderer => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <AccountConfigurationTab
        row={row}
        disableControls={false}
        editor={editor}
        onCopyText={onCopyText}
      />
    );
  });
  return renderer;
};

describe('AccountConfigurationTab', () => {
  it('keeps the initial configuration state free of animated loading icons', () => {
    const editor = makeEditor('codex');
    editor.state = null;
    editor.draft = null;

    const renderer = renderTab(makeRow('codex'), editor);

    expect(findLoadingSpinners(renderer)).toHaveLength(0);
    expect(readText(renderer.toJSON())).toContain('accounts.config_loading');
  });

  it('keeps credential refresh out of the configuration toolbar', () => {
    const renderer = renderTab(makeRow('codex'), makeEditor('codex'));

    expect(renderer.root.findAllByProps({ 'data-account-config-reload': 'toolbar' })).toHaveLength(
      0
    );
    expect(readText(renderer.toJSON())).not.toContain('common.refresh');
  });

  it('shows xAI routing choices, Base URL, and WebSocket controls together', () => {
    const draft = makeDraft({
      xaiRoutingMode: 'official-api',
      baseUrl: 'https://api.x.ai/v1',
      websockets: true,
    });
    const renderer = renderTab(makeRow('xai'), makeEditor('xai', draft));
    const text = readText(renderer.toJSON());

    expect(text).toContain('accounts.config_xai_route_grok');
    expect(text).toContain('accounts.config_xai_route_official');
    expect(
      renderer.root
        .findAllByType(Input)
        .some((input) => input.props.label === 'accounts.config_xai_base_url')
    ).toBe(true);
    expect(text).toContain('auth_files.websockets_label');
    expect(text).not.toContain('ai_providers.claude_cloak_mode_label');
  });

  it('shows Claude cloak controls without xAI or websocket fields', () => {
    const renderer = renderTab(makeRow('claude'), makeEditor('claude'));
    const text = readText(renderer.toJSON());

    expect(text).toContain('ai_providers.claude_cloak_mode_label');
    expect(text).toContain('accounts.config_tool_prefix_disabled');
    expect(text).not.toContain('accounts.config_xai_route_mode');
    expect(text).not.toContain('auth_files.websockets_label');
  });

  it('keeps primary save and reset actions at the top of the hierarchy', () => {
    const editor = makeEditor('codex');
    editor.dirty = true;
    editor.canSave = true;
    const renderer = renderTab(makeRow('codex'), editor);
    const buttons = renderer.root.findAllByType('button');
    const resetButton = buttons.find((button) => readText(button).includes('common.reset'));
    const saveButton = buttons.find((button) => readText(button).includes('common.save'));
    if (!resetButton || !saveButton) throw new Error('configuration toolbar actions missing');

    expect(resetButton.props.className).toContain('configurationToolbarButton');
    expect(saveButton.props.className).toContain('configurationToolbarButton');
    act(() => resetButton.props.onClick());
    act(() => saveButton.props.onClick());

    expect(editor.reset).toHaveBeenCalledTimes(1);
    expect(editor.save).toHaveBeenCalledTimes(1);
  });

  it('allows an administratively disabled credential to edit and save configuration', () => {
    const editor = makeEditor('codex');
    editor.dirty = true;
    editor.canSave = true;
    const renderer = renderTab(makeRow('codex', { disabled: true }), editor);
    const saveButton = renderer.root
      .findAllByType('button')
      .find((button) => readText(button).includes('common.save'));
    if (!saveButton) throw new Error('configuration save action missing');

    expect(renderer.root.findAllByType(Input).every((input) => input.props.disabled !== true)).toBe(
      true
    );
    expect(saveButton.props.disabled).toBe(false);
    expect(readText(renderer.toJSON())).not.toContain('accounts.config_disabled_read_only');

    act(() => saveButton.props.onClick());
    expect(editor.save).toHaveBeenCalledTimes(1);
  });

  it.each(['enabled', 'inherit'] as const)(
    'forwards the %s cooling policy selection to the credential editor',
    (policy) => {
      const editor = makeEditor('codex');
      const renderer = renderTab(makeRow('codex'), editor);

      act(() => renderer.root.findByType(CoolingPolicySelect).props.onChange(policy));

      expect(editor.updateField).toHaveBeenCalledWith('disableCooling', policy);
    }
  );

  it('uses a compact hierarchy while keeping field-level guidance', () => {
    const renderer = renderTab(makeRow('codex'), makeEditor('codex'));
    const text = readText(renderer.toJSON());
    const region = renderer.root.findByProps({ role: 'region' });
    const noteInput = renderer.root
      .findAllByType(Input)
      .find((input) => input.props.label === 'auth_files.note_label');

    expect(region.props['aria-label']).toBe('accounts.detail_tab_config');
    expect(text).not.toContain('accounts.config_title');
    expect(text).not.toContain('accounts.config_desc');
    expect(text).not.toContain('accounts.config_section_routing_desc');
    expect(text).not.toContain('accounts.config_section_scheduling_desc');
    expect(text).not.toContain('accounts.config_section_provider_desc');
    expect(text).not.toContain('accounts.config_section_models_desc');
    expect(text).not.toContain('accounts.config_section_advanced_desc');
    expect(text).not.toContain('accounts.config_aliases_hint');
    expect(text).toContain('accounts.config_excluded_models_hint');
    expect(noteInput?.props.hint).toBeUndefined();
  });

  it('exposes the selected saved record through a compact redacted raw-data disclosure', () => {
    const onCopyText = vi.fn();
    const editor = makeEditor('xai');
    editor.rawDataText = JSON.stringify(
      {
        type: 'xai',
        using_api: true,
        access_token: '[redacted]',
      },
      null,
      2
    );
    const renderer = renderTab(makeRow('xai'), editor, onCopyText);
    const details = renderer.root.findByType('details');
    const summary = details.findByType('summary');
    const rawData = renderer.root.findByProps({ 'aria-label': 'accounts.config_view_raw_data' });
    const copyButton = renderer.root
      .findAllByType('button')
      .find((button) => readText(button).includes('common.copy'));
    if (!copyButton) throw new Error('raw data copy action missing');

    expect(details.props.open).toBeUndefined();
    expect(readText(summary)).toContain('accounts.config_view_raw_data');
    expect(readText(summary)).toContain('accounts.config_raw_data_redacted');
    expect(readText(rawData)).toContain('"using_api": true');
    expect(readText(rawData)).toContain('"access_token": "[redacted]"');

    act(() => copyButton.props.onClick());
    expect(onCopyText).toHaveBeenCalledWith(editor.rawDataText);
  });

  it('keeps a shared single-object source read-only while retaining raw-data access', () => {
    const editor = makeEditor('claude');
    editor.sourceMemberCount = 2;
    editor.sharedSourceReadOnly = true;
    editor.canSave = false;
    const renderer = renderTab(makeRow('claude'), editor);
    const text = readText(renderer.toJSON());

    expect(text).toContain('accounts.config_shared_source_read_only');
    expect(text).toContain('accounts.config_excluded_models_shared_hint');
    expect(renderer.root.findAllByType(Input).every((input) => input.props.disabled)).toBe(true);
    expect(
      renderer.root.findAllByType('textarea').every((textarea) => textarea.props.disabled === true)
    ).toBe(true);
    expect(renderer.root.findByType('details')).toBeDefined();
    expect(
      renderer.root
        .findAllByType('button')
        .find((button) => readText(button).includes('common.save'))?.props.disabled
    ).toBe(true);
  });

  it('explains why runtime-only credentials cannot be configured', () => {
    const renderer = renderTab(makeRow('aistudio', { runtimeOnly: true }), {
      ...makeEditor('aistudio'),
      state: null,
      draft: null,
    });
    const text = readText(renderer.toJSON());

    expect(text).toContain('accounts.detail_runtime_config_unavailable');
    expect(text).toContain('accounts.config_runtime_only_desc');
    expect(text).not.toContain('common.save');
  });
});
