import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { buildProviderRows } from '../ProviderTable/rowData';
import type { ProviderRecentUsageMap } from '../utils';

const mocks = vi.hoisted(() => ({
  fetchModelsViaApiCall: vi.fn(),
  fetchV1ModelsViaApiCall: vi.fn(),
  fetchClaudeModelsViaApiCall: vi.fn(),
  fetchGeminiModelsViaApiCall: vi.fn(),
}));

vi.mock('@/services/api', () => ({
  modelsApi: mocks,
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
    title,
    closeDisabled,
  }: {
    open: boolean;
    children: ReactNode;
    footer?: ReactNode;
    title?: ReactNode;
    closeDisabled?: boolean;
  }) =>
    open
      ? createElement(
          'div',
          { 'data-testid': 'health-drawer-shell', 'data-close-disabled': String(Boolean(closeDisabled)) },
          title,
          children,
          footer
        )
      : null,
}));

import { ProviderHealthCheckDrawer } from './ProviderHealthCheckDrawer';

import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const emptyUsageByProvider = new Map() as ProviderRecentUsageMap;
const renderers: ReactTestRenderer[] = [];

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe('ProviderHealthCheckDrawer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.fetchClaudeModelsViaApiCall.mockResolvedValue([{ name: 'claude-sonnet-4-5' }]);
  });

  afterEach(async () => {
    for (const renderer of renderers.splice(0)) {
      await act(async () => {
        renderer.unmount();
      });
    }
  });

  it('displays selected scope banner, alias metadata, and open config trigger', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude 主力池',
          apiKey: 'sk-ant-1234567890',
          baseUrl: 'https://claude.example.com',
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });
    const onOpenProviderConfig = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderHealthCheckDrawer
          open
          rows={rows}
          actionsDisabled={false}
          scopeMode="selected"
          onOpenProviderConfig={onOpenProviderConfig}
          onClose={vi.fn()}
          onApplyResultActions={vi.fn()}
          onSetProviderEnabled={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    const textContent = renderer!.root
      .findAll((node) => typeof node.children?.[0] === 'string')
      .flatMap((node) => node.children.filter((item): item is string => typeof item === 'string'))
      .join(' ');

    expect(textContent).toContain(
      i18n.t('ai_providers.health_check_scope_selected_badge', { defaultValue: '已选范围' })
    );
    expect(textContent).toContain('Claude · Claude 主力池');
    expect(textContent).toContain(rows[0].maskedKey);
    expect(textContent).toContain('claude.example.com');

    const configLabel = i18n.t('ai_providers.health_check_open_config', { defaultValue: '配置' });
    const configButton = renderer!.root
      .findAllByType('button')
      .find((button) =>
        button.findAllByType('span').some((span) => span.children.join('') === configLabel)
      );
    expect(configButton).toBeDefined();

    await act(async () => {
      configButton!.props.onClick();
    });
    expect(onOpenProviderConfig).toHaveBeenCalledTimes(1);
    expect(onOpenProviderConfig).toHaveBeenCalledWith(rows[0]);
  });

  it('locks close and footer actions while running, and retains toggled rows via allRows under parent status filter', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Primary',
          apiKey: 'sk-ant-1',
          baseUrl: 'https://primary.example.com',
        },
        {
          name: 'Claude Backup',
          apiKey: 'sk-ant-2',
          baseUrl: 'https://backup.example.com',
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let resolveFirst!: (value: Array<{ name: string }>) => void;
    mocks.fetchClaudeModelsViaApiCall.mockImplementationOnce(
      () =>
        new Promise<Array<{ name: string }>>((resolve) => {
          resolveFirst = resolve;
        })
    );
    mocks.fetchClaudeModelsViaApiCall.mockResolvedValueOnce([{ name: 'claude-sonnet-4-5' }]);

    const onSetProviderEnabled = vi.fn().mockResolvedValue(undefined);
    const onOpenProviderConfig = vi.fn();

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderHealthCheckDrawer
          open
          rows={rows}
          allRows={rows}
          actionsDisabled={false}
          onOpenProviderConfig={onOpenProviderConfig}
          onClose={vi.fn()}
          onApplyResultActions={vi.fn()}
          onSetProviderEnabled={onSetProviderEnabled}
        />
      );
    });

    // While running === true: closeDisabled is true and footer batch buttons are disabled
    const runningShell = renderer.root.findByProps({ 'data-testid': 'health-drawer-shell' });
    expect(runningShell.props['data-close-disabled']).toBe('true');

    const findFooterBtn = (text: string) =>
      renderer.root.findAllByType('button').find((button) =>
        button.findAllByType('span').some((span) =>
          span.children.some((child) => typeof child === 'string' && child.includes(text))
        )
      );

    const rerunLabel = i18n.t('ai_providers.health_check_rerun', { defaultValue: '重新检测' });
    const enableAllLabel = i18n.t('ai_providers.health_check_enable_all', { defaultValue: '全部启用' });
    const disableAllLabel = i18n.t('ai_providers.health_check_disable_all', { defaultValue: '全部停用' });
    const applyLabel = i18n.t('ai_providers.health_check_apply_results', { defaultValue: '按检测结果应用' });

    expect(findFooterBtn(rerunLabel)?.props.disabled).toBe(true);
    expect(findFooterBtn(enableAllLabel)?.props.disabled).toBe(true);
    expect(findFooterBtn(disableAllLabel)?.props.disabled).toBe(true);
    expect(findFooterBtn(applyLabel)?.props.disabled).toBe(true);

    await act(async () => {
      resolveFirst([{ name: 'claude-sonnet-4-5' }]);
    });
    await flush();

    expect(
      renderer.root.findByProps({ 'data-testid': 'health-drawer-shell' }).props['data-close-disabled']
    ).toBe('false');
    expect(findFooterBtn(enableAllLabel)?.props.disabled).toBe(false);
    expect(findFooterBtn(disableAllLabel)?.props.disabled).toBe(false);

    // Simulate parent statusFilter='enabled': toggling rows[0] disabled removes it from filtered `rows`,
    // while `allRows` retains rows[0] with updated enabled=false
    const switches = renderer.root.findAllByType(ToggleSwitch);
    expect(switches).toHaveLength(2);
    await act(async () => {
      switches[0].props.onChange(false);
    });
    await flush();

    const updatedRow0 = { ...rows[0], enabled: false };
    await act(async () => {
      renderer.update(
        <ProviderHealthCheckDrawer
          open
          rows={[rows[1]]}
          allRows={[updatedRow0, rows[1]]}
          actionsDisabled={false}
          onOpenProviderConfig={onOpenProviderConfig}
          onClose={vi.fn()}
          onApplyResultActions={vi.fn()}
          onSetProviderEnabled={onSetProviderEnabled}
        />
      );
    });
    await flush();

    const switchesAfter = renderer.root.findAllByType(ToggleSwitch);
    expect(switchesAfter).toHaveLength(2);
    expect(switchesAfter[0].props.checked).toBe(false);
    expect(switchesAfter[1].props.checked).toBe(true);

    const configLabel = i18n.t('ai_providers.health_check_open_config', { defaultValue: '配置' });
    const configButtons = renderer.root
      .findAllByType('button')
      .filter((button) =>
        button.findAllByType('span').some((span) => span.children.join('') === configLabel)
      );
    expect(configButtons).toHaveLength(2);
    await act(async () => {
      configButtons[0].props.onClick();
    });
    expect(onOpenProviderConfig).toHaveBeenCalledWith(updatedRow0);

    // Re-running checks still checks both snapshotted rows even though rows[0] is filtered out of `rows`
    mocks.fetchClaudeModelsViaApiCall.mockResolvedValue([{ name: 'claude-sonnet-4-5' }]);
    await act(async () => {
      findFooterBtn(rerunLabel)!.props.onClick();
    });
    await flush();
    expect(renderer.root.findAllByType(ToggleSwitch)).toHaveLength(2);
  });
});
