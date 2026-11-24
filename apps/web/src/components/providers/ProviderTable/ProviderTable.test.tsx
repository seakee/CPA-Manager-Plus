import { act } from 'react';
import { create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { ProviderKeyConfig } from '@/types';
import { buildRecentRequestCompositeKey } from '@/utils/recentRequests';
import { ProviderStatusBar } from '../ProviderStatusBar';
import type { ProviderRecentUsageMap } from '../utils';
import { ProviderCardGrid } from './ProviderCardGrid';
import { buildProviderRows, type ProviderRow } from './rowData';
import { filterAndSortProviderRows } from './sort';
import { ProviderTable, type ProviderTableProps } from './ProviderTable';
import { maskApiKey } from '@/utils/format';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
  }),
}));

const emptyInput = {
  gemini: [],
  interactions: [],
  codex: [],
  xai: [],
  claude: [],
  vertex: [],
  openai: [],
  usageByProvider: new Map() as ProviderRecentUsageMap,
};

const getRows = (renderer: ReactTestRenderer) =>
  renderer.root.findAll(
    (node) => node.type === 'div' && node.props.role === 'row' && node.props.tabIndex === 0
  );

const getHeaderRow = (renderer: ReactTestRenderer) => {
  const header = renderer.root.findAll(
    (node) => node.type === 'div' && node.props.role === 'row' && node.props.tabIndex === undefined
  )[0];
  if (!header) throw new Error('Header row not found');
  return header;
};

const getText = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : getText(child))).join('');

const clickButton = (button: ReactTestInstance) => {
  const onClick = button.props.onClick as ((event: { stopPropagation: () => void }) => void) | undefined;
  if (!onClick) throw new Error('Button click handler not found');

  act(() => {
    onClick({ stopPropagation: vi.fn() });
  });
};

const toggleSwitch = (toggle: ReactTestInstance, value: boolean) => {
  const onChange = toggle.props.onChange as ((value: boolean) => void) | undefined;
  if (!onChange) throw new Error('Toggle change handler not found');

  act(() => {
    onChange(value);
  });
};

const changeInput = (input: ReactTestInstance, value: string) => {
  const onChange = input.props.onChange as
    | ((event: { target: { value: string } }) => void)
    | undefined;
  if (!onChange) throw new Error('Input change handler not found');

  act(() => {
    onChange({ target: { value } });
  });
};

const blurInput = (input: ReactTestInstance) => {
  const onBlur = input.props.onBlur as (() => void) | undefined;
  if (!onBlur) throw new Error('Input blur handler not found');

  act(() => {
    onBlur();
  });
};

const keyDownInput = (input: ReactTestInstance, key: string) => {
  const onKeyDown = input.props.onKeyDown as
    | ((event: {
        key: string;
        preventDefault: () => void;
        currentTarget: { blur: () => void };
      }) => void)
    | undefined;
  const onBlur = input.props.onBlur as (() => void) | undefined;
  if (!onKeyDown || !onBlur) throw new Error('Input keyboard handlers not found');

  act(() => {
    onKeyDown({
      key,
      preventDefault: vi.fn(),
      currentTarget: {
        blur: onBlur,
      },
    });
  });
};

const getPriorityEditTrigger = (row: ReactTestInstance) => {
  const trigger = row
    .findAll((node) => node.type === 'button')
    .find(
      (button) =>
        button.props.type === 'button' &&
        button.props['aria-label'] === 'ai_providers.priority_edit'
    );
  if (!trigger) throw new Error('Priority edit trigger not found');
  return trigger;
};

const getPriorityInputs = (row: ReactTestInstance) =>
  row
    .findAll((node) => node.type === 'input')
    .filter((node) => node.props['aria-label'] === 'ai_providers.priority_edit');

const getPriorityInput = (row: ReactTestInstance) => {
  const input = getPriorityInputs(row)[0];
  if (!input) throw new Error('Priority input not found');
  return input;
};

const openPriorityEditor = (renderer: ReactTestRenderer, rowIndex = 0) => {
  clickButton(getPriorityEditTrigger(getRows(renderer)[rowIndex]));
  return getPriorityInput(getRows(renderer)[rowIndex]);
};

describe('ProviderTable', () => {
  const codexConfigs: ProviderKeyConfig[] = [
    { apiKey: 'low-key', baseUrl: 'https://low.example.com/v1', priority: 1 },
    {
      apiKey: 'disabled-key',
      baseUrl: 'https://disabled.example.com/v1',
      priority: 99,
      excludedModels: ['*'],
    },
    { apiKey: 'high-key', baseUrl: 'https://high.example.com/v1', priority: 9 },
    { apiKey: 'unset-key', baseUrl: 'https://unset.example.com/v1' },
  ];

  const renderTable = (
    rows: ProviderRow[],
    overrides: Partial<ProviderTableProps> = {}
  ) => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProviderTable
          rows={rows}
          loading={false}
          actionsDisabled={false}
          toggleDisabled={false}
          resolvedTheme="light"
          emptyState={<div>empty</div>}
          onInspect={overrides.onInspect}
          onOpenModels={overrides.onOpenModels}
          onOpenConnectivity={overrides.onOpenConnectivity}
          onHealthCheckRow={overrides.onHealthCheckRow}
          onEdit={overrides.onEdit ?? (() => {})}
          onDelete={overrides.onDelete ?? (() => {})}
          onToggleEnabled={overrides.onToggleEnabled}
          onChangePriority={overrides.onChangePriority}
          routingStrategy={overrides.routingStrategy}
          globalDisableCooling={overrides.globalDisableCooling}
          {...overrides}
        />
      );
    });
    return renderer;
  };

  it('keeps sorted row actions mapped to original config indexes', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    const onOpenModels = vi.fn();
    const onOpenConnectivity = vi.fn();
    const onHealthCheckRow = vi.fn();
    const onToggleEnabled = vi.fn();
    const onInspect = vi.fn();

    const renderer = renderTable(rows, {
      onEdit,
      onDelete,
      onOpenModels,
      onOpenConnectivity,
      onHealthCheckRow,
      onToggleEnabled,
      onInspect,
    });

    const renderedRows = getRows(renderer);
    expect(renderedRows).toHaveLength(4);

    expect(getText(renderedRows[0])).toContain('high.example.com/v1');
    expect(getText(renderedRows[0])).not.toContain('https://high.example.com/v1');
    expect(getText(renderedRows[renderedRows.length - 1])).toContain('disabled.example.com/v1');

    const probeButton = renderedRows[0]
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === '测活');
    expect(probeButton).toBeTruthy();
    clickButton(probeButton!);
    expect(onHealthCheckRow).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );

    const editButton = renderedRows[0]
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === 'common.edit');
    expect(editButton).toBeTruthy();
    clickButton(editButton!);
    expect(onEdit).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );

    const moreMenu = renderedRows[0].findByType(DropdownMenu);
    const menuItems = moreMenu.props.items as DropdownMenuItem[];
    const modelsMenuItem = menuItems.find((item) => item.key === 'models' && item.type !== 'divider');
    const connectivityMenuItem = menuItems.find(
      (item) => item.key === 'connectivity' && item.type !== 'divider'
    );
    const deleteMenuItem = menuItems.find((item) => item.key === 'delete' && item.type !== 'divider');
    expect(modelsMenuItem).toBeTruthy();
    expect(connectivityMenuItem).toBeTruthy();
    expect(deleteMenuItem).toMatchObject({ tone: 'danger' });

    act(() => {
      if (modelsMenuItem && modelsMenuItem.type !== 'divider') modelsMenuItem.onClick();
    });
    expect(onOpenModels).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );

    act(() => {
      if (connectivityMenuItem && connectivityMenuItem.type !== 'divider') {
        connectivityMenuItem.onClick();
      }
    });
    expect(onOpenConnectivity).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );

    act(() => {
      if (deleteMenuItem && deleteMenuItem.type !== 'divider') deleteMenuItem.onClick();
    });
    expect(onDelete).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );

    toggleSwitch(renderedRows[0].findByType(ToggleSwitch), false);
    expect(onToggleEnabled).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 }),
      false
    );

    act(() => {
      renderedRows[0].props.onClick();
    });
    expect(onInspect).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 })
    );
  });

  it('displays provider alias, masked key preview, prefix badge, capability chips, and official default endpoint fallback', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          name: 'Codex Alias One',
          apiKey: 'sk-codex-secret-1234',
          prefix: 'cx',
          proxyUrl: 'http://127.0.0.1:7890',
          weight: 3,
          cloak: { mode: 'always' },
          disableCooling: true,
        },
      ],
      xai: [
        {
          name: 'xAI Realtime',
          apiKey: 'xai-secret-9876',
          websockets: true,
          disableCooling: false,
          models: [{ name: 'grok-4', alias: 'g4' }, { name: 'grok-4-mini' }],
          excludedModels: ['grok-legacy'],
        },
      ],
    });

    const onOpenModels = vi.fn();
    const renderer = renderTable(rows, {
      onOpenModels,
      selectedDrawerKey: rows[0].key,
      routingStrategy: 'round-robin',
      globalDisableCooling: false,
    });
    const renderedRows = getRows(renderer);

    const firstRowText = getText(renderedRows[0]);
    expect(firstRowText).toContain('Codex Alias One');
    expect(firstRowText).toContain(rows[0].maskedKey);
    expect(firstRowText).toContain('前缀cx/');
    expect(firstRowText).toContain('代理');
    expect(firstRowText).toContain('伪装');
    expect(firstRowText).not.toContain('权重 3');
    expect(firstRowText).toContain('不冷却');
    expect(firstRowText).toContain('官方默认端点');
    expect(firstRowText).toContain('通配全开');
    expect(String(renderedRows[0].props.className)).toContain('rowSelected');
    expect(renderedRows[0].props['aria-selected']).toBe(true);

    const secondRowText = getText(renderedRows[1]);
    expect(secondRowText).toContain('xAI Realtime');
    expect(secondRowText).toContain('WebSocket');
    expect(secondRowText).not.toContain('允许冷却');
    expect(secondRowText).toContain('g4');
    expect(secondRowText).toContain('grok-4-mini');
    expect(secondRowText).not.toContain('+1');
    expect(secondRowText).toContain('1 条别名');
    expect(secondRowText).toContain('排除 1');

    const modelsPill = renderedRows[1]
      .findAll((node) => node.type === 'button')
      .find((node) => String(node.props['aria-label'] ?? '').includes('2 模型'));
    expect(modelsPill).toBeTruthy();
    clickButton(modelsPill!);
    expect(onOpenModels).toHaveBeenLastCalledWith(
      expect.objectContaining({ alias: 'xAI Realtime' })
    );
  });

  it('falls back models pill to onEdit when onOpenModels is omitted and renders 0 for disabled zero-model rows', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          name: 'Disabled Empty',
          apiKey: 'sk-disabled',
          excludedModels: ['*', 'gpt-legacy'],
        },
      ],
    });
    const onEdit = vi.fn();
    const renderer = renderTable(rows, { onEdit });
    const renderedRows = getRows(renderer);

    expect(getText(renderedRows[0])).toContain('0');
    expect(getText(renderedRows[0])).toContain('排除 1');
    expect(getText(renderedRows[0])).not.toContain('通配全开');

    const modelsPill = renderedRows[0]
      .findAll((node) => node.type === 'button')
      .find((node) => String(node.props['aria-label'] ?? '').includes('ai_providers.table_col_models'));
    expect(modelsPill).toBeTruthy();
    clickButton(modelsPill!);
    expect(onEdit).toHaveBeenCalledWith(rows[0]);
  });

  it('supports batch selection checkboxes in selectionMode', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onToggleSelectRow = vi.fn();
    const onToggleSelectAllPage = vi.fn();
    const onInspect = vi.fn();

    const renderer = renderTable(rows, {
      selectionMode: true,
      selectedKeys: new Set([rows[0].key]),
      onToggleSelectRow,
      onToggleSelectAllPage,
      onInspect,
    });

    const headerCheckbox = getHeaderRow(renderer).findByType(SelectionCheckbox);
    expect(headerCheckbox.props.checked).toBe(false);
    act(() => {
      headerCheckbox.props.onChange(true);
    });
    expect(onToggleSelectAllPage).toHaveBeenCalledWith(true);

    const renderedRows = getRows(renderer);
    const firstRowCheckbox = renderedRows[0].findByType(SelectionCheckbox);
    expect(firstRowCheckbox.props.checked).toBe(true);
    expect(renderedRows[0].props['aria-selected']).toBe(true);

    act(() => {
      renderedRows[1].props.onClick();
    });
    expect(onToggleSelectRow).toHaveBeenCalledWith(rows[1].key);
    expect(onInspect).not.toHaveBeenCalled();
  });

  it('marks disabled rows and renders disabled toggle state', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const renderer = renderTable(rows);

    const lastRow = getRows(renderer)[3];
    const lastToggle = lastRow.findByType(ToggleSwitch);
    expect(lastToggle.props.checked).toBe(false);
  });

  it('shows priority as an edit trigger before entering edit mode', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const renderer = renderTable(rows);

    const firstRow = getRows(renderer)[0];
    const priorityTrigger = getPriorityEditTrigger(firstRow);
    expect(getText(priorityTrigger)).toBe('9');
    expect(getPriorityInputs(firstRow)).toHaveLength(0);

    const priorityInput = openPriorityEditor(renderer);
    expect(priorityInput.props.value).toBe('9');
  });

  it('commits a direct priority edit on blur without opening row detail', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onChangePriority = vi.fn();
    const onInspect = vi.fn();
    const renderer = renderTable(rows, { onChangePriority, onInspect });

    const priorityInput = openPriorityEditor(renderer);

    changeInput(priorityInput, '42');
    blurInput(priorityInput);

    expect(onChangePriority).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 }),
      42
    );
    expect(onInspect).not.toHaveBeenCalled();
  });

  it('commits a direct priority edit with Enter only once', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onChangePriority = vi.fn();
    const renderer = renderTable(rows, { onChangePriority });

    const priorityInput = openPriorityEditor(renderer);

    changeInput(priorityInput, '12');
    keyDownInput(priorityInput, 'Enter');

    expect(onChangePriority).toHaveBeenCalledTimes(1);
    expect(onChangePriority).toHaveBeenLastCalledWith(
      expect.objectContaining({ kind: 'codex', originalIndex: 2 }),
      12
    );
  });

  it('cancels a direct priority edit with Escape without committing on blur', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onChangePriority = vi.fn();
    const renderer = renderTable(rows, { onChangePriority });

    const priorityInput = openPriorityEditor(renderer);

    changeInput(priorityInput, '12');
    keyDownInput(priorityInput, 'Escape');

    const updatedTrigger = getPriorityEditTrigger(getRows(renderer)[0]);
    expect(getText(updatedTrigger)).toBe('9');
    expect(onChangePriority).not.toHaveBeenCalled();
  });

  it('restores blank or invalid direct priority drafts without committing', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onChangePriority = vi.fn();
    const renderer = renderTable(rows, { onChangePriority });

    let priorityInput = openPriorityEditor(renderer);

    changeInput(priorityInput, '');
    blurInput(priorityInput);
    let priorityTrigger = getPriorityEditTrigger(getRows(renderer)[0]);
    expect(getText(priorityTrigger)).toBe('9');

    priorityInput = openPriorityEditor(renderer);
    changeInput(priorityInput, 'not-a-number');
    blurInput(priorityInput);
    priorityTrigger = getPriorityEditTrigger(getRows(renderer)[0]);
    expect(getText(priorityTrigger)).toBe('9');
    expect(onChangePriority).not.toHaveBeenCalled();
  });

  it('does not submit unchanged direct priority edits', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const onChangePriority = vi.fn();
    const renderer = renderTable(rows, { onChangePriority });

    const priorityInput = openPriorityEditor(renderer);

    changeInput(priorityInput, '9');
    blurInput(priorityInput);

    expect(onChangePriority).not.toHaveBeenCalled();
  });

  it('disables inline priority controls with the rest of row actions', () => {
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs })
    );
    const renderer = renderTable(rows, { actionsDisabled: true });

    const firstRow = getRows(renderer)[0];
    const priorityTrigger = getPriorityEditTrigger(firstRow);

    expect(priorityTrigger.props.disabled).toBe(true);
    expect(getPriorityInputs(firstRow)).toHaveLength(0);
  });

  it('renders the provided empty state when there are no rows', () => {
    const renderer = renderTable([]);
    expect(getText(renderer.root as unknown as ReactTestInstance)).toContain('empty');
  });

  it('distinguishes unavailable and partial OpenAI statistics from no traffic and shows key count pills', () => {
    const usageByProvider: ProviderRecentUsageMap = new Map([
      [
        'mixed-keyless',
        new Map([
          [
            buildRecentRequestCompositeKey('https://mixed-keyless.example/v1', 'tracked-key'),
            { success: 5, failed: 1, recentRequests: [] },
          ],
        ]),
      ],
    ]);
    const rows = buildProviderRows({
      ...emptyInput,
      openai: [
        {
          name: 'pure-keyless',
          baseUrl: 'https://pure-keyless.example/v1',
          apiKeyEntries: [],
        },
        {
          name: 'mixed-keyless',
          baseUrl: 'https://mixed-keyless.example/v1',
          apiKeyEntries: [
            { apiKey: 'tracked-key' },
            { apiKey: '', proxyUrl: 'socks5://proxy:1080' },
          ],
        },
      ],
      usageByProvider,
    });
    const renderer = renderTable(rows);
    const renderedRows = getRows(renderer);

    expect(getText(renderedRows[0])).toContain('无密钥');
    expect(getText(renderedRows[0])).toContain('ai_providers.usage_stats_unavailable');
    expect(getText(renderedRows[0])).not.toContain('status_bar.no_requests');

    expect(getText(renderedRows[1])).not.toContain('1 把密钥');
    expect(getText(renderedRows[1])).toContain('6 次');
    expect(getText(renderedRows[1])).toContain('83.3%');
    expect(getText(renderedRows[1])).toContain('ai_providers.usage_stats_partial');
  });

  it('renders an all-idle CPA status bar without a no-requests text placeholder for zero-traffic rows', () => {
    const usageByProvider: ProviderRecentUsageMap = new Map([
      [
        'codex',
        new Map([
          [
            buildRecentRequestCompositeKey('https://high.example.com/v1', 'high-key'),
            { success: 82, failed: 6, recentRequests: [] },
          ],
        ]),
      ],
    ]);
    const rows = filterAndSortProviderRows(
      buildProviderRows({ ...emptyInput, codex: codexConfigs, usageByProvider })
    );
    const renderer = renderTable(rows);
    const renderedRows = getRows(renderer);

    expect(getText(renderedRows[0])).toContain('88 次');
    expect(getText(renderedRows[0])).toContain('93.2%');
    expect(renderedRows[0].findAllByType(ProviderStatusBar)).toHaveLength(1);
    expect(getText(renderedRows[0])).not.toContain('status_bar.no_requests');

    expect(getText(renderedRows[1])).not.toContain('status_bar.no_requests');
    const idleStatusBars = renderedRows[1].findAllByType(ProviderStatusBar);
    expect(idleStatusBars).toHaveLength(1);
    expect(idleStatusBars[0].props.statusData.blocks).toHaveLength(10);
    expect(idleStatusBars[0].props.statusData.blocks.every((block: string) => block === 'idle')).toBe(
      true
    );
  });
});

describe('ProviderCardGrid', () => {
  it('renders cards with alias, wildcard/excluded badges, selection checkboxes, and action callbacks', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          name: 'Card Codex',
          apiKey: 'sk-card-1',
          prefix: 'card',
          excludedModels: ['o1-mini'],
        },
      ],
      openai: [
        {
          name: 'Card OpenAI',
          baseUrl: 'https://card.example/v1',
          apiKeyEntries: [{ apiKey: 'k1' }, { apiKey: 'k2' }],
          models: [{ name: 'gpt-5' }],
        },
      ],
    });

    const onInspect = vi.fn();
    const onOpenModels = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    const onToggleEnabled = vi.fn();
    const onToggleSelectRow = vi.fn();

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProviderCardGrid
          rows={rows}
          resolvedTheme="dark"
          selectionMode
          selectedKeys={new Set([rows[0].key])}
          selectedDrawerKey={rows[1].key}
          onToggleSelectRow={onToggleSelectRow}
          onInspect={onInspect}
          onOpenModels={onOpenModels}
          onEdit={onEdit}
          onDelete={onDelete}
          onToggleEnabled={onToggleEnabled}
        />
      );
    });

    const cards = renderer.root.findAll(
      (node) => node.type === 'article' && node.props.role === 'listitem'
    );
    expect(cards).toHaveLength(2);
    expect(cards[0].props['aria-selected']).toBeUndefined();
    expect(cards[1].props['aria-selected']).toBeUndefined();
    expect(getText(cards[0])).toContain('Card Codex');
    expect(getText(cards[0])).toContain('通配全开');
    expect(getText(cards[0])).toContain('排除 1');
    expect(getText(cards[0])).toContain('官方默认端点');
    expect(getText(cards[0])).toContain('可用');
    expect(getText(cards[1])).toContain('Card OpenAI');
    expect(getText(cards[1])).toContain('2 把密钥');
    expect(getText(cards[1])).toContain('gpt-5');
    expect(getText(cards[1])).not.toContain('1 模型');

    act(() => {
      cards[1].props.onClick();
    });
    expect(onToggleSelectRow).toHaveBeenCalledWith(rows[1].key);
    expect(onInspect).not.toHaveBeenCalled();

    const detailBtn = cards[1]
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === 'ai_providers.detail_button');
    clickButton(detailBtn!);
    expect(onInspect).toHaveBeenCalledWith(rows[1]);

    const modelsBtn = cards[1]
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === '模型配置');
    clickButton(modelsBtn!);
    expect(onOpenModels).toHaveBeenCalledWith(rows[1]);

    const deleteBtn = cards[1]
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === 'common.delete');
    clickButton(deleteBtn!);
    expect(onDelete).toHaveBeenCalledWith(rows[1]);

    expect(cards[1].findAllByType(DropdownMenu)).toHaveLength(0);
  });

  it('renders 4-tile historical usage KPI, recent status counts, error badge/banner, and inline priority editing', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      claude: [
        {
          name: 'Card Error Claude',
          apiKey: 'sk-card-err',
          priority: 12,
          disableCooling: true,
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
        },
      ],
    });
    rows[0].stats = { success: 19, failure: 4 };
    rows[0].statsSource = 'history';
    rows[0].latestFailure = {
      statusCode: 503,
      errorCode: 'upstream_overloaded',
      summary: 'Service Unavailable',
      timestampMs: Date.UTC(2026, 3, 18, 12, 0, 0),
      isMostRecent: true,
    };
    rows[0].history = {
      matched: true,
      totalRequests: 23,
      successCalls: 19,
      failureCalls: 4,
      totalTokens: 319_600,
      totalCost: 0.08,
      successRate: 82.6,
      lastSeenMs: Date.UTC(2026, 3, 18, 12, 0, 0),
      recentRequests: [],
      recentStatusData: rows[0].statusData,
    };
    const onChangePriority = vi.fn();
    const onHealthCheckRow = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProviderCardGrid
          rows={rows}
          resolvedTheme="light"
          onChangePriority={onChangePriority}
          onHealthCheckRow={onHealthCheckRow}
          onEdit={onEdit}
          onDelete={onDelete}
        />
      );
    });

    const card = renderer.root.findAll(
      (node) => node.type === 'article' && node.props.role === 'listitem'
    )[0];
    const text = getText(card);
    expect(text).toContain('异常 503');
    expect(text).toContain('不冷却');
    expect(text).toContain('历史用量');
    expect(text).toContain('23');
    expect(text).toContain('319.6K');
    expect(text).toContain('$0.08');
    expect(text).toContain('82.6%');
    expect(text).toContain('最近状态');
    expect(text).toContain('503 · Service Unavailable');

    const healthBtn = card
      .findAllByType(Button)
      .find((button) => button.props['aria-label'] === '测活');
    clickButton(healthBtn!);
    expect(onHealthCheckRow).toHaveBeenCalledWith(rows[0]);

    const priorityBtn = card
      .findAll((node) => node.type === 'button')
      .find((btn) => String(btn.props['aria-label'] ?? '').includes('12'));
    act(() => {
      priorityBtn!.props.onClick({ stopPropagation: vi.fn() });
    });
    const input = card.find((node) => node.type === 'input' && node.props.type === 'number');
    act(() => {
      input.props.onChange({ target: { value: '25' } });
    });
    act(() => {
      input.props.onBlur();
    });
    expect(onChangePriority).toHaveBeenCalledWith(rows[0], 25);
  });
  it('matches rows when searching by displayed maskedKey for single-key and OpenAI providers', () => {
    const geminiKey = 'sk-gemini-visible-9988';
    const openaiKey = 'sk-openai-visible-7766';
    const rows = buildProviderRows({
      ...emptyInput,
      gemini: [{ name: 'Masked Gemini', apiKey: geminiKey }],
      openai: [
        {
          name: 'Masked OpenAI',
          baseUrl: 'https://openai.example/v1',
          apiKeyEntries: [{ apiKey: openaiKey }],
        },
      ],
    });

    const geminiMatched = filterAndSortProviderRows(rows, {
      kind: 'all',
      statusFilter: 'all',
      healthFilter: 'all',
      searchText: maskApiKey(geminiKey),
      selectedModels: new Set(),
      sortOption: 'priority',
      sortDirection: 'asc',
    });
    expect(geminiMatched.map((row) => row.label)).toEqual(['Masked Gemini']);

    const openaiMatched = filterAndSortProviderRows(rows, {
      kind: 'all',
      statusFilter: 'all',
      healthFilter: 'all',
      searchText: maskApiKey(openaiKey),
      selectedModels: new Set(),
      sortOption: 'priority',
      sortDirection: 'asc',
    });
    expect(openaiMatched.map((row) => row.label)).toEqual(['Masked OpenAI']);
  });
});
describe('ProviderTable cooling & SQLite history cells', () => {
  it('renders cooling chip in scheduling cell and SQLite history tokens/cost in recent column', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      claude: [
        {
          name: 'Claude Official',
          apiKey: 'sk-ant-1234',
          authIndex: 'a26021ea73d8d103',
          disableCooling: true,
          models: [{ name: 'claude-sonnet-4-5' }, { name: 'claude-opus-4-5' }],
        },
      ],
    });
    rows[0].stats = { success: 19, failure: 1 };
    rows[0].statsSource = 'history';
    rows[0].latestFailure = {
      statusCode: 429,
      summary: 'rate limit',
      timestampMs: 1_700_000_000_000,
      isMostRecent: true,
    };
    rows[0].history = {
      matched: true,
      totalRequests: 20,
      successCalls: 19,
      failureCalls: 1,
      totalTokens: 351450,
      totalCost: 0.72,
      successRate: 95,
      lastSeenMs: 1_700_000_000_000,
      recentRequests: [],
      recentStatusData: rows[0].statusData,
    };

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProviderTable
          rows={rows}
          resolvedTheme="light"
          onEdit={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    const row = getRows(renderer)[0];
    const cells = row.findAll((node) => node.type === 'div' && node.props.role === 'cell');
    expect(getText(cells[1])).not.toContain('不冷却');
    expect(getText(cells[3])).toContain('不冷却');
    expect(getText(cells[4])).toContain('351.4K');
    expect(getText(cells[4])).toContain('$0.72');
    expect(getText(cells[4])).toContain('429');
    expect(getText(cells[4])).toContain('20 次');
    expect(getText(cells[4])).toContain('95%');
    const recentWrap = cells[4].findAll((node) => node.type === 'div')[1];
    expect(recentWrap.findAllByType(ProviderStatusBar)).toHaveLength(1);
  });

  it('renders secondary models preview, explicit global cooling override, and triggers onInspect from more menu', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      claude: [
        {
          name: 'Claude Official',
          apiKey: 'sk-ant-1234',
          disableCooling: false,
          weight: 4,
          models: [
            { name: 'claude-sonnet-4-5' },
            { name: 'claude-opus-4-5' },
            { name: 'claude-haiku-4-5' },
          ],
        },
      ],
    });
    const onInspect = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProviderTable
          rows={rows}
          resolvedTheme="light"
          routingStrategy="weighted-round-robin"
          globalDisableCooling
          onInspect={onInspect}
          onEdit={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    const row = getRows(renderer)[0];

    expect(getText(row)).toContain('claude-sonnet-4-5');
    expect(getText(row)).toContain('claude-opus-4-5');
    expect(getText(row)).toContain('+1');
    expect(getText(row)).toContain('共 3 个模型');
    expect(getText(row)).toContain('权重 4');
    expect(getText(row)).not.toContain('未生效');
    expect(getText(row)).toContain('允许冷却');

    const moreMenu = row.findByType(DropdownMenu);
    const inspectItem = (moreMenu.props.items as DropdownMenuItem[]).find(
      (item) => item.key === 'inspect' && item.type !== 'divider'
    );
    expect(inspectItem).toBeTruthy();
    act(() => {
      if (inspectItem && inspectItem.type !== 'divider') inspectItem.onClick();
    });
    expect(onInspect).toHaveBeenCalledWith(rows[0]);
  });
});
