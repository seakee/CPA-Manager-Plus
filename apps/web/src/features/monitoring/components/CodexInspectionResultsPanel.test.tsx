import type { ComponentProps } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import type {
  CodexInspectionResultItem,
  CodexInspectionRunResult,
} from '@/features/monitoring/codexInspection';
import inspectionStyles from '@/features/monitoring/CodexInspectionPage.module.scss';
import tooltipStyles from './FailureDetailsTooltip.module.scss';
import { CodexInspectionResultsPanel } from './CodexInspectionResultsPanel';

const t = ((key: string, options?: Record<string, unknown>) => {
  if (options?.percent) return `${key}:${options.percent}`;
  if (options?.count !== undefined) return `${key}:${options.count}`;
  return key;
}) as never;

const createItem = (
  overrides: Partial<CodexInspectionResultItem> = {}
): CodexInspectionResultItem => ({
  key: 'credential-1',
  fileName: 'codex-account-free.json',
  displayAccount: 'account@example.com',
  authIndex: null,
  accountId: null,
  provider: 'codex',
  disabled: false,
  autoRecoverOwned: false,
  status: 'active',
  state: 'enabled',
  raw: {} as AuthFileItem,
  action: 'keep',
  actionReason: 'healthy quota does not require handling',
  statusCode: 200,
  usedPercent: 3,
  isQuota: false,
  autoRecoverEligible: false,
  error: '',
  errorKind: '',
  quotaWindows: [
    {
      id: 'monthly',
      labelKey: 'monthly',
      usedPercent: 3,
      resetLabel: '',
      limitWindowSeconds: null,
    },
  ],
  ...overrides,
});

const renderPanel = (
  item: CodexInspectionResultItem,
  overrides: Partial<ComponentProps<typeof CodexInspectionResultsPanel>> = {}
) => {
  let renderer: ReactTestRenderer;
  act(() => {
    renderer = create(
      <CodexInspectionResultsPanel
        result={{} as CodexInspectionRunResult}
        filteredResults={[item]}
        pendingActionCount={0}
        handlingFilterCounts={{ all: 1, pending: 0, no_action: 1 }}
        filterCounts={{ all: 1, delete: 0, disable: 0, enable: 0, reauth: 0, keep: 1 }}
        handlingFilter="all"
        actionFilter="all"
        pagination={{
          currentPage: 1,
          totalPages: 1,
          pageItems: [item],
          startItem: 1,
          endItem: 1,
          count: 1,
        }}
        pageSize={10}
        pageSizeOptions={[10, 20]}
        executing={false}
        isInspectionInFlight={false}
        t={t}
        onActionFilterChange={vi.fn()}
        onHandlingFilterChange={vi.fn()}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
        onExecutePlanned={vi.fn()}
        onExecuteSingle={vi.fn()}
        filterLabel={(filter) => filter}
        handlingFilterLabel={(filter) => filter}
        {...overrides}
      />
    );
  });
  return renderer!;
};

const collectText = (renderer: ReactTestRenderer) =>
  renderer.root
    .findAll((node) => typeof node.children[0] === 'string')
    .flatMap((node) => node.children.filter((child): child is string => typeof child === 'string'));

describe('CodexInspectionResultsPanel', () => {
  it('renders one four-section result card without duplicated healthy copy or empty actions', () => {
    const renderer = renderPanel(createItem());
    const text = collectText(renderer);

    expect(renderer.root.findAllByType('table')).toHaveLength(0);
    expect(renderer.root.findAllByType('article')).toHaveLength(1);
    expect(renderer.root.findAllByType('section')).toHaveLength(4);
    expect(text).toContain('monitoring.codex_inspection_quota_remaining:97%');
    expect(text).not.toContain('healthy quota does not require handling');
    expect(text).not.toContain('—');
  });

  it('places a custom server operation inside the same result card', () => {
    const renderer = renderPanel(
      createItem({ action: 'delete', actionReason: 'invalid account' }),
      {
        pendingActionCount: 1,
        renderOperation: () => <button data-testid="custom-operation">execute</button>,
      }
    );

    expect(renderer.root.findByProps({ 'data-testid': 'custom-operation' })).toBeDefined();
    expect(renderer.root.findAllByType('article')).toHaveLength(1);
  });

  it('renders the xAI probe HTTP status when billing health returns one', () => {
    const renderer = renderPanel(
      createItem({
        provider: 'xai',
        statusCode: 200,
        errorKind: 'billing_healthy',
      })
    );

    expect(renderer.root.findAll((node) => node.children.join('') === 'HTTP 200')).toHaveLength(1);
  });

  it('moves failed probe diagnostics into the failure status tooltip', () => {
    const rawResponse = '{"code":"personal-team-blocked:spending-limit"}';
    const renderer = renderPanel(
      createItem({
        provider: 'xai',
        statusCode: 402,
        errorKind: 'http_status',
        error: rawResponse,
      })
    );
    const text = collectText(renderer);
    const tooltip = renderer.root.findByProps({ role: 'tooltip' });

    expect(text).toContain('monitoring.codex_inspection_probe_state_failed');
    expect(text).toContain('monitoring.codex_inspection_error_summary_http_status');
    expect(text).toContain(rawResponse);
    expect(tooltip.props.className).toContain(tooltipStyles.tooltip);
    expect(renderer.root.findAllByType('details')).toHaveLength(0);
  });

  it('keeps all action filters visible and renders the plan without a label prefix', () => {
    const renderer = renderPanel(createItem({ planType: 'free' }));
    const text = collectText(renderer);

    expect(text).toEqual(
      expect.arrayContaining(['all', 'reauth', 'delete', 'disable', 'enable', 'keep'])
    );
    expect(
      renderer.root.findAllByProps({
        'aria-label': 'monitoring.codex_inspection_action_filter_label',
      })
    ).toHaveLength(1);
    expect(text).not.toContain('monitoring.codex_inspection_action_filter_label');
    expect(text).not.toEqual(expect.arrayContaining(['pending', 'no_action']));
    expect(text).toContain('codex_quota.plan_free');
    expect(text).not.toContain('codex_quota.plan_label');
  });

  it('gives disabled credential state chips a visible neutral background', () => {
    const renderer = renderPanel(createItem({ disabled: true }));
    const stateChip = renderer.root.find(
      (node) => node.children.join('') === 'monitoring.codex_inspection_state_disabled'
    );

    expect(stateChip.props.className).toContain(inspectionStyles.stateDisabled);
  });

  it('localizes keyed conclusion reasons without repeating a conclusion heading', () => {
    const translatedT = ((key: string) => `translated:${key}`) as never;
    const renderer = renderPanel(
      createItem({
        action: 'disable',
        actionReason: 'monitoring.codex_inspection_reason_quota_threshold',
      }),
      { t: translatedT }
    );
    const text = collectText(renderer);

    expect(text).toContain('translated:monitoring.codex_inspection_reason_quota_threshold');
    expect(text).not.toContain('translated:monitoring.codex_inspection_conclusion');
    expect(
      renderer.root.findAll((node) =>
        String(node.props.className ?? '').includes(inspectionStyles.actionBadge)
      )
    ).toHaveLength(1);
  });
});
