import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import type { ComponentProps } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { UseAuthFileConfigurationEditorResult } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import type { AuthFileConfigurationDraft } from '@/features/authFiles/model/authFileConfiguration';
import { AccountModelsTab } from './AccountModelsTab';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: Record<string, unknown>) => {
        if (!options) return key;
        const params = Object.entries(options)
          .map(([name, value]) => `${name}=${String(value)}`)
          .join(',');
        return `${key}:${params}`;
      },
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

const makeRow = (overrides: Partial<AccountRow> = {}): AccountRow =>
  ({
    key: 'credential.json\u0000codex-1',
    selectionKey: 'credential.json\u0000codex-1',
    fileName: 'credential.json',
    accountLabel: 'account@example.com',
    provider: 'codex',
    planType: null,
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: 'codex-1',
    projectId: '',
    priority: null,
    createdAtMs: null,
    updatedAtMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    raw: { name: 'credential.json', type: 'codex', provider: 'codex' },
    ...overrides,
  }) as AccountRow;

const makeEditor = ({
  rules = '',
  originalRules = rules,
  prefix = '',
  originalPrefix = prefix,
  dirty = false,
  canSave = false,
}: {
  rules?: string;
  originalRules?: string;
  prefix?: string;
  originalPrefix?: string;
  dirty?: boolean;
  canSave?: boolean;
} = {}): UseAuthFileConfigurationEditorResult => {
  const originalDraft = makeDraft({ excludedModelsText: originalRules, prefix: originalPrefix });
  const draft = makeDraft({ excludedModelsText: rules, prefix });
  return {
    state: {
      authFile: { name: 'credential.json', type: 'codex' },
      fileName: 'credential.json',
      loading: false,
      saving: false,
      error: '',
      record: { type: 'codex' },
      recordIndex: null,
      providerKey: 'codex',
      originalDraft,
      draft,
    },
    draft,
    errors: {},
    dirty,
    canSave,
    rawDataText: '{}',
    sourceMemberCount: 1,
    sharedSourceReadOnly: false,
    updateField: vi.fn(),
    reset: vi.fn(),
    reload: vi.fn(async () => undefined),
    save: vi.fn(async () => undefined),
  };
};

type AccountModelsTabProps = ComponentProps<typeof AccountModelsTab>;

const renderTab = (
  overrides: Partial<AccountModelsTabProps> = {}
): { renderer: ReactTestRenderer; props: AccountModelsTabProps } => {
  const props: AccountModelsTabProps = {
    row: makeRow(),
    disableControls: false,
    fileName: 'credential.json',
    fileType: 'codex',
    loading: false,
    refreshing: false,
    error: null,
    models: [],
    modelDefinitions: [],
    modelDefinitionsLoading: false,
    modelDefinitionsError: null,
    globalExcluded: {},
    globalExcludedState: 'ready',
    aliases: {},
    editor: makeEditor(),
    onRefresh: vi.fn(),
    onManageGlobalRules: vi.fn(),
    onOpenAdvancedRules: vi.fn(),
    onCopyText: vi.fn(),
    ...overrides,
  };
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<AccountModelsTab {...props} />);
  });
  return { renderer, props };
};

const findModelRow = (renderer: ReactTestRenderer, modelId: string): ReactTestInstance => {
  const row = renderer.root
    .findAllByType('article')
    .find((candidate) => readText(candidate).includes(modelId));
  if (!row) throw new Error(`Model row missing: ${modelId}`);
  return row;
};

const findButtonByText = (root: ReactTestInstance, label: string): ReactTestInstance => {
  const button = root
    .findAllByType('button')
    .find((candidate) => readText(candidate).includes(label));
  if (!button) throw new Error(`Button missing: ${label}`);
  return button;
};

const findModelSwitch = (row: ReactTestInstance): ReactTestInstance => row.findByType(ToggleSwitch);

const flipModelSwitch = (row: ReactTestInstance, enabled: boolean) => {
  act(() => {
    findModelSwitch(row).props.onChange(enabled);
  });
};

const findListMenuItem = (renderer: ReactTestRenderer, key: string) => {
  const items = renderer.root.findByType(DropdownMenu).props.items as DropdownMenuItem[];
  const item = items.find((candidate) => candidate.key === key);
  if (!item || item.type === 'divider') throw new Error(`Menu item missing: ${key}`);
  return item;
};

const hasUnsavedMarker = (row: ReactTestInstance) =>
  row.findAll((node) => node.props['aria-label'] === 'accounts.config_unsaved').length > 0;

const findLoadingSpinners = (renderer: ReactTestRenderer) =>
  renderer.root.findAll(
    (node) =>
      typeof node.props.className === 'string' &&
      node.props.className.split(/\s+/).includes('loading-spinner')
  );

describe('AccountModelsTab', () => {
  it('does not render animated icons for automatic model loading', () => {
    const editor = makeEditor();
    editor.state = { ...editor.state!, loading: true };

    const { renderer } = renderTab({
      loading: true,
      modelDefinitionsLoading: true,
      globalExcludedState: 'loading',
      editor,
    });

    expect(findLoadingSpinners(renderer)).toHaveLength(0);
  });

  it('keeps the refresh spinner for an explicit model refresh', () => {
    const { renderer } = renderTab({ loading: true, refreshing: true });

    expect(findLoadingSpinners(renderer)).toHaveLength(1);
    expect(findListMenuItem(renderer, 'refresh-models').disabled).toBe(true);
  });

  it('moves list-level actions into the model list menu', () => {
    const onRefresh = vi.fn();
    const onManageGlobalRules = vi.fn();
    const { renderer } = renderTab({ onRefresh, onManageGlobalRules });

    act(() => {
      findListMenuItem(renderer, 'refresh-models').onClick();
      findListMenuItem(renderer, 'manage-global-rules').onClick();
    });
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(onManageGlobalRules).toHaveBeenCalledTimes(1);
    expect(readText(renderer.toJSON())).not.toContain('accounts.detail_models_summary');
  });

  it('uses the credential detail models label for its region', () => {
    const { renderer } = renderTab();

    expect(renderer.root.findByProps({ role: 'region' }).props['aria-label']).toBe(
      'accounts.detail_tab_models'
    );
  });

  it('distinguishes available, credential, global, and combined exclusions', () => {
    const editor = makeEditor({ rules: 'credential-model\nboth-model' });
    const onManageGlobalRules = vi.fn();
    const { renderer } = renderTab({
      editor,
      models: [
        { id: 'available-model' },
        { id: 'credential-model' },
        { id: 'global-model' },
        { id: 'both-model' },
      ],
      globalExcluded: { codex: ['global-model', 'both-model'] },
      onManageGlobalRules,
    });

    expect(renderer.root.findAllByProps({ 'data-model-scope': 'available' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'credential' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'global' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'both' })).toHaveLength(1);

    expect(findModelSwitch(findModelRow(renderer, 'available-model')).props.checked).toBe(true);
    expect(findModelSwitch(findModelRow(renderer, 'credential-model')).props.checked).toBe(false);
    // A model off only by a global rule has no credential switch to flip.
    expect(findModelSwitch(findModelRow(renderer, 'global-model')).props.disabled).toBe(true);
    act(() => {
      findButtonByText(
        findModelRow(renderer, 'global-model'),
        'accounts.model_locked_global'
      ).props.onClick();
    });
    expect(onManageGlobalRules).toHaveBeenCalledTimes(1);
    expect(editor.updateField).not.toHaveBeenCalled();

    // Its own exact rule can still be removed; the global rule stays named beside it.
    const bothRow = findModelRow(renderer, 'both-model');
    expect(readText(bothRow)).toContain('accounts.model_locked_global');
    flipModelSwitch(bothRow, true);
    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', 'credential-model');
  });

  it('adds and removes exact credential rules from row actions', () => {
    const disableEditor = makeEditor({ rules: 'existing-model' });
    const disabled = renderTab({
      editor: disableEditor,
      models: [{ id: 'existing-model' }, { id: 'new-model' }],
    });

    flipModelSwitch(findModelRow(disabled.renderer, 'new-model'), false);
    expect(disableEditor.updateField).toHaveBeenCalledWith(
      'excludedModelsText',
      'existing-model\nnew-model'
    );

    const restoreEditor = makeEditor({ rules: 'existing-model' });
    const restored = renderTab({
      editor: restoreEditor,
      models: [{ id: 'existing-model' }],
    });
    flipModelSwitch(findModelRow(restored.renderer, 'existing-model'), true);
    expect(restoreEditor.updateField).toHaveBeenCalledWith('excludedModelsText', '');
  });

  it('routes wildcard credential rules to advanced configuration without deleting them', () => {
    const editor = makeEditor({ rules: 'gpt-5-*' });
    const onOpenAdvancedRules = vi.fn();
    const { renderer } = renderTab({
      editor,
      models: [],
      modelDefinitions: [{ id: 'gpt-5-mini' }],
      onOpenAdvancedRules,
    });

    expect(readText(renderer.toJSON())).toContain('gpt-5-*');
    const wildcardRow = findModelRow(renderer, 'gpt-5-mini');
    expect(findModelSwitch(wildcardRow).props.disabled).toBe(true);
    act(() => {
      findButtonByText(wildcardRow, 'accounts.model_locked_advanced').props.onClick();
    });
    expect(onOpenAdvancedRules).toHaveBeenCalledTimes(1);
    expect(editor.updateField).not.toHaveBeenCalled();
  });

  it('displays and searches every alias mapped to the same model', () => {
    const { renderer } = renderTab({
      editor: makeEditor({ rules: 'claude-sonnet' }),
      models: [{ id: 'gpt-5-codex' }, { id: 'claude-sonnet' }],
      aliases: {
        codex: [
          { name: 'gpt-5-codex', alias: 'fast-alias' },
          { name: 'gpt-5-codex', alias: 'secondary-alias' },
        ],
      },
    });

    const search = renderer.root.findByType(Input);
    act(() => {
      search.props.onChange({ target: { value: 'secondary-alias' } });
    });
    expect(renderer.root.findAllByType('article')).toHaveLength(1);
    expect(readText(renderer.root.findAllByType('article')[0])).toContain('gpt-5-codex');
    expect(readText(renderer.root.findAllByType('article')[0])).toContain('fast-alias');
    expect(readText(renderer.root.findAllByType('article')[0])).toContain('secondary-alias');

    act(() => {
      search.props.onChange({ target: { value: '' } });
      renderer.root.findByProps({ id: 'account-model-filter-disabled' }).props.onClick({
        preventDefault: vi.fn(),
      });
    });
    expect(renderer.root.findAllByType('article')).toHaveLength(1);
    expect(readText(renderer.root.findAllByType('article')[0])).toContain('claude-sonnet');
  });

  it('uses Gemini aliases and global exclusions regardless of the configured provider key', () => {
    const onCopyText = vi.fn();
    const { renderer } = renderTab({
      row: makeRow({
        provider: 'gemini-cli',
        raw: { name: 'credential.json', type: 'gemini-cli', provider: 'gemini-cli' },
      }),
      fileType: 'gemini-cli',
      models: [{ id: 'Gemini-2.5-Pro' }],
      globalExcluded: { gemini: ['gemini-2.5-pro'] },
      aliases: { gemini: [{ name: 'gemini-2.5-pro', alias: 'team-gemini' }] },
      onCopyText,
    });
    const modelRow = findModelRow(renderer, 'Gemini-2.5-Pro');

    expect(modelRow.props['data-model-scope']).toBe('global');
    expect(readText(modelRow)).toContain('team-gemini');
    act(() => {
      findButtonByText(modelRow, 'Gemini-2.5-Pro').props.onClick();
    });
    expect(onCopyText).toHaveBeenCalledWith('Gemini-2.5-Pro');
  });

  it('marks rows with staged rule changes and leaves save to the drawer', () => {
    const editor = makeEditor({
      originalRules: 'model-a\nmodel-b',
      rules: 'model-b\nmodel-c',
      dirty: true,
      canSave: true,
    });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'model-a' }, { id: 'model-b' }, { id: 'model-c' }],
    });

    expect(hasUnsavedMarker(findModelRow(renderer, 'model-a'))).toBe(true);
    expect(hasUnsavedMarker(findModelRow(renderer, 'model-b'))).toBe(false);
    expect(hasUnsavedMarker(findModelRow(renderer, 'model-c'))).toBe(true);
    // Save / discard live in the drawer footer pill, not inside the tab.
    const buttonText = renderer.root.findAllByType('button').map(readText).join(' ');
    expect(buttonText).not.toContain('common.save');
    expect(buttonText).not.toContain('common.reset');
  });

  it('does not mark rows for unsaved non-model configuration changes', () => {
    const { renderer } = renderTab({
      editor: makeEditor({ rules: 'model-a', dirty: true, canSave: true }),
      models: [{ id: 'model-a' }],
    });

    expect(hasUnsavedMarker(findModelRow(renderer, 'model-a'))).toBe(false);
  });

  it('turns every editable shown model on or off from the list menu', () => {
    const editor = makeEditor({ rules: 'model-b' });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'model-a' }, { id: 'model-b' }, { id: 'model-c' }, { id: 'global-model' }],
      globalExcluded: { codex: ['global-model'] },
    });

    act(() => {
      findListMenuItem(renderer, 'turn-off-shown').onClick();
    });
    expect(editor.updateField).toHaveBeenLastCalledWith(
      'excludedModelsText',
      'model-a\nmodel-b\nmodel-c'
    );

    act(() => {
      findListMenuItem(renderer, 'turn-on-shown').onClick();
    });
    expect(editor.updateField).toHaveBeenLastCalledWith('excludedModelsText', '');
  });

  it('keeps runtime-only and administratively disabled credentials read-only', () => {
    const runtimeEditor = makeEditor();
    runtimeEditor.state = null;
    runtimeEditor.draft = null;
    const runtimeOnly = renderTab({
      row: makeRow({ runtimeOnly: true }),
      editor: runtimeEditor,
      models: [{ id: 'runtime-model' }],
    });
    const runtimeSwitch = findModelSwitch(findModelRow(runtimeOnly.renderer, 'runtime-model'));
    expect(readText(runtimeOnly.renderer.toJSON())).toContain('accounts.config_runtime_only_desc');
    expect(runtimeSwitch.props.disabled).toBe(true);

    const disabledEditor = makeEditor({ canSave: true });
    const disabled = renderTab({
      row: makeRow({ disabled: true }),
      editor: disabledEditor,
      models: [{ id: 'disabled-credential-model' }],
    });
    expect(readText(disabled.renderer.toJSON())).toContain('accounts.config_disabled_read_only');
    expect(
      findModelSwitch(findModelRow(disabled.renderer, 'disabled-credential-model')).props.disabled
    ).toBe(true);
  });

  it('shows partial-state warnings and retries a failed credential configuration load', () => {
    const editor = makeEditor();
    if (!editor.state) throw new Error('Expected editor state');
    editor.state = {
      ...editor.state,
      error: 'load failed',
      record: null,
      draft: null,
    };
    editor.draft = null;
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'visible-model' }],
      globalExcludedState: 'error',
      modelDefinitionsError: 'failed',
    });
    const text = readText(renderer.toJSON());

    expect(text).toContain('accounts.model_config_unavailable');
    expect(text).toContain('accounts.model_global_rules_unavailable');
    expect(text).toContain('accounts.model_definitions_partial');
    act(() => {
      findButtonByText(renderer.root, 'common.retry').props.onClick();
    });
    expect(editor.reload).toHaveBeenCalledTimes(1);
    expect(findModelSwitch(findModelRow(renderer, 'visible-model')).props.disabled).toBe(true);
  });

  it('waits for global exclusion rules before enabling credential model actions', () => {
    const editor = makeEditor();
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'pending-model' }],
      globalExcludedState: 'loading',
    });

    expect(readText(renderer.toJSON())).toContain('accounts.model_global_rules_loading');
    expect(findModelSwitch(findModelRow(renderer, 'pending-model')).props.disabled).toBe(true);
    expect(editor.updateField).not.toHaveBeenCalled();
  });

  it('does not classify models as available while global exclusion state is unknown', () => {
    const editor = makeEditor({ rules: 'credential-model' });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'unknown-model' }, { id: 'credential-model' }],
      globalExcludedState: 'error',
    });

    expect(renderer.root.findAllByProps({ 'data-model-scope': 'unknown' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'credential' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'available' })).toHaveLength(0);
    expect(findModelSwitch(findModelRow(renderer, 'unknown-model')).props.disabled).toBe(true);
    expect(findModelSwitch(findModelRow(renderer, 'credential-model')).props.disabled).toBe(true);

    act(() => {
      renderer.root.findByProps({ id: 'account-model-filter-available' }).props.onClick({
        preventDefault: vi.fn(),
      });
    });
    expect(renderer.root.findAllByType('article')).toHaveLength(0);

    act(() => {
      renderer.root.findByProps({ id: 'account-model-filter-disabled' }).props.onClick({
        preventDefault: vi.fn(),
      });
    });
    expect(renderer.root.findAllByType('article')).toHaveLength(1);
    expect(readText(renderer.root.findAllByType('article')[0])).toContain('credential-model');
    expect(editor.updateField).not.toHaveBeenCalled();
  });

  it('labels single-object plugin exclusions as shared and blocks child-level edits', () => {
    const editor = makeEditor({ rules: 'shared-model\nboth-model' });
    editor.sourceMemberCount = 2;
    editor.sharedSourceReadOnly = true;
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'available-model' }, { id: 'shared-model' }, { id: 'both-model' }],
      globalExcluded: { codex: ['both-model'] },
    });

    expect(readText(renderer.toJSON())).toContain('accounts.config_shared_source_read_only');
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'shared' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-model-scope': 'shared-global' })).toHaveLength(1);
    expect(readText(findModelRow(renderer, 'shared-model'))).toContain(
      'accounts.model_rule_count_shared'
    );
    for (const modelId of ['shared-model', 'available-model']) {
      const row = findModelRow(renderer, modelId);
      expect(readText(row)).toContain('accounts.model_locked_shared');
      expect(findModelSwitch(row).props.disabled).toBe(true);
    }
    expect(editor.updateField).not.toHaveBeenCalled();
  });

  it('keeps cached or projected models visible after a runtime refresh failure', () => {
    const { renderer } = renderTab({
      error: 'failed',
      models: [{ id: 'last-known-model' }],
    });

    expect(readText(renderer.toJSON())).toContain('accounts.model_load_failed');
    expect(findModelRow(renderer, 'last-known-model')).toBeDefined();
  });

  it('writes bare canonical rule id when disabling prefixed runtime model (UI Case 1)', () => {
    const editor = makeEditor({ originalPrefix: 'haochi', prefix: 'haochi' });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    flipModelSwitch(findModelRow(renderer, 'haochi/gpt-5.5'), false);

    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', 'gpt-5.5');
    expect(editor.updateField).not.toHaveBeenCalledWith('excludedModelsText', 'haochi/gpt-5.5');
  });

  it('writes bare canonical rule id when credential prefix has leading and trailing slashes', () => {
    const editor = makeEditor({ originalPrefix: '/haochi/', prefix: '/haochi/' });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    flipModelSwitch(findModelRow(renderer, 'haochi/gpt-5.5'), false);

    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', 'gpt-5.5');
  });

  it('normalizes legacy prefixed exact rule when disabling model (UI Case 2)', () => {
    const editor = makeEditor({
      rules: 'haochi/gpt-5.5',
      originalRules: 'haochi/gpt-5.5',
      originalPrefix: 'haochi',
      prefix: 'haochi',
    });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    const modelRow = findModelRow(renderer, 'haochi/gpt-5.5');
    expect(modelRow.props['data-model-scope']).not.toBe('credential');
    expect(modelRow.props['data-model-scope']).toBe('available');

    flipModelSwitch(modelRow, false);

    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', 'gpt-5.5');
  });

  it('restores canonical rule for prefixed model (UI Case 3)', () => {
    const editor = makeEditor({
      rules: 'gpt-5.5',
      originalRules: 'gpt-5.5',
      originalPrefix: 'haochi',
      prefix: 'haochi',
    });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    const modelRow = findModelRow(renderer, 'haochi/gpt-5.5');
    expect(modelRow.props['data-model-scope']).toBe('credential');

    flipModelSwitch(modelRow, true);

    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', '');
  });

  it('uses original saved prefix instead of unsaved draft prefix to interpret runtime models (UI Case 4)', () => {
    const editor = makeEditor({
      originalPrefix: 'haochi',
      prefix: 'team',
      rules: '',
    });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    flipModelSwitch(findModelRow(renderer, 'haochi/gpt-5.5'), false);

    expect(editor.updateField).toHaveBeenCalledWith('excludedModelsText', 'gpt-5.5');
  });

  it('disables credential actions and warns when prefixed runtime models cannot be mapped (UI Case 5)', () => {
    const editor = makeEditor({ originalPrefix: 'haochi', prefix: 'haochi' });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'haochi/private-model' }],
      modelDefinitions: [],
      modelDefinitionsLoading: false,
    });

    const modelRow = findModelRow(renderer, 'haochi/private-model');
    expect(modelRow.props['data-model-scope']).toBe('unknown');

    const unmappedSwitch = findModelSwitch(modelRow);
    expect(unmappedSwitch.props.disabled).toBe(true);

    act(() => {
      unmappedSwitch.props.onChange(false);
    });
    expect(editor.updateField).not.toHaveBeenCalled();

    const text = readText(renderer.toJSON());
    expect(text).toContain('accounts.model_rule_mapping_unavailable');
  });

  it('preserves both runtime routes without merging and reflects shared exclusion state (UI Case 6)', () => {
    const editor = makeEditor({
      rules: 'gpt-5.5',
      originalRules: 'gpt-5.5',
      originalPrefix: 'haochi',
      prefix: 'haochi',
    });
    const { renderer } = renderTab({
      editor,
      models: [{ id: 'gpt-5.5' }, { id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    const articles = renderer.root.findAllByType('article');
    expect(articles).toHaveLength(2);

    const bareRow = findModelRow(renderer, 'gpt-5.5');
    const prefixedRow = findModelRow(renderer, 'haochi/gpt-5.5');

    expect(bareRow.props['data-model-scope']).toBe('credential');
    expect(prefixedRow.props['data-model-scope']).toBe('credential');
  });
});
