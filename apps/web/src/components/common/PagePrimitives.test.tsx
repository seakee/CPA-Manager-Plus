import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { AppFooter } from './AppFooter';
import {
  APP_AUTHOR_URL,
  APP_RELEASES_URL,
  APP_REPOSITORY_URL,
} from './appFooterMeta';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { EmptyState } from '@/components/ui/EmptyState';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { FloatingActionDock } from './FloatingActionDock';
import { MetricSummaryCard, MetricSummaryGrid } from './MetricSummaryCard';
import { PageControlBar } from './PageControlBar';
import { PageTransitionLayerContext } from './PageTransitionLayer';
import { PaginationFooter } from './PaginationFooter';
import { WorkspaceListPanel } from './WorkspaceListPanel';
import { SectionActionFooter } from './SectionActionFooter';

describe('Page header and footer primitives', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('collapses PageControlBar toolbar row when toolbar is null and attaches it when provided', () => {
    let collapsed!: ReactTestRenderer;
    act(() => {
      collapsed = create(
        <PageControlBar
          testId="header-bar"
          toolbarTestId="header-toolbar"
          leading={<span>Tabs</span>}
          actions={<button type="button">Refresh</button>}
          toolbar={null}
        />
      );
    });

    expect(collapsed.root.findAllByProps({ 'data-testid': 'header-bar' })).toHaveLength(1);
    expect(collapsed.root.findAllByProps({ 'data-testid': 'header-toolbar' })).toHaveLength(0);

    let expanded!: ReactTestRenderer;
    act(() => {
      expanded = create(
        <PageControlBar
          testId="header-bar"
          toolbarTestId="header-toolbar"
          leading={<span>Tabs</span>}
          actions={<button type="button">Refresh</button>}
          toolbar={<input aria-label="Search" />}
        />
      );
    });

    expect(expanded.root.findAllByProps({ 'data-testid': 'header-toolbar' })).toHaveLength(1);
  });

  it('clamps PaginationFooter page jumps and disables boundary navigation buttons', () => {
    const onPageChange = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <PaginationFooter
          currentPage={1}
          totalPages={4}
          infoText="1-20 / 75"
          prevLabel="Prev"
          nextLabel="Next"
          onPageChange={onPageChange}
          showPageJump
          jumpInputAriaLabel="Jump to page"
          testIds={{
            root: 'pager-root',
            prev: 'pager-prev',
            next: 'pager-next',
            jumpInput: 'pager-jump',
          }}
        />
      );
    });

    const prevBtn = renderer.root.findByProps({ 'data-testid': 'pager-prev' });
    const nextBtn = renderer.root.findByProps({ 'data-testid': 'pager-next' });
    expect(prevBtn.props.disabled).toBe(true);
    expect(nextBtn.props.disabled).toBe(false);

    const jumpInput = renderer.root.findByProps({ 'data-testid': 'pager-jump' });
    act(() => {
      jumpInput.props.onChange({ target: { value: '99' } });
    });
    act(() => {
      jumpInput.props.onBlur();
    });

    expect(onPageChange).toHaveBeenCalledWith(4);
  });

  it('suppresses FloatingActionDock on non-current transition layers and syncs dock height css var', () => {
    const props = new Map<string, string>();
    vi.stubGlobal('document', {
      documentElement: {
        style: {
          setProperty: (key: string, value: string) => props.set(key, value),
          removeProperty: (key: string) => props.delete(key),
        },
      },
    });

    let exiting!: ReactTestRenderer;
    act(() => {
      exiting = create(
        <PageTransitionLayerContext.Provider
          value={{ status: 'exiting', isCurrentLayer: false, isAnimating: true }}
        >
          <FloatingActionDock open testId="batch-dock" leading="2 selected">
            <button type="button">Delete</button>
          </FloatingActionDock>
        </PageTransitionLayerContext.Provider>
      );
    });
    expect(exiting.toJSON()).toBeNull();
    expect(props.has('--page-floating-dock-height')).toBe(false);

    let active!: ReactTestRenderer;
    act(() => {
      active = create(
        <PageTransitionLayerContext.Provider
          value={{ status: 'current', isCurrentLayer: true, isAnimating: false }}
        >
          <FloatingActionDock open testId="batch-dock" leading="2 selected">
            <button type="button">Delete</button>
          </FloatingActionDock>
        </PageTransitionLayerContext.Provider>
      );
    });
    expect(active.root.findAllByProps({ 'data-testid': 'batch-dock' })).toHaveLength(1);
    expect(props.get('--page-floating-dock-height')).toBe('72px');

    act(() => {
      active.unmount();
    });
    expect(props.has('--page-floating-dock-height')).toBe(false);
  });

  it('renders SectionActionFooter status and action slot', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <SectionActionFooter testId="group-footer" status="1 unsaved change">
          <button type="button">Save</button>
        </SectionActionFooter>
      );
    });

    expect(renderer.root.findAllByProps({ 'data-testid': 'group-footer' })).toHaveLength(1);
  });

  it('renders MetricSummaryCard, EmptyState canvas, and joined group SegmentedTabs with allowReselect', () => {
    const onSelect = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <div>
          <MetricSummaryCard label="Calls" value="42" meta="3 accounts" icon="calls" accent="blue" />
          <EmptyState variant="canvas" title="No data" footer={<span>Diagnostic hint</span>} />
          <SegmentedTabs
            variant="joined"
            roleMode="group"
            allowReselect
            ariaLabel="Range"
            activeTab="custom"
            onChange={onSelect}
            items={[
              { id: 'today', label: 'Today' },
              { id: 'custom', label: 'Custom', ariaLabel: 'Custom' },
            ]}
          />
        </div>
      );
    });

    expect(renderer.root.findAllByProps({ 'data-summary-icon': 'calls' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-variant': 'canvas' })).toHaveLength(1);
    const customBtn = renderer.root.findByProps({ 'aria-label': 'Custom' });
    expect(customBtn.props['aria-pressed']).toBe(true);
    act(() => {
      customBtn.props.onClick({ preventDefault: vi.fn() });
    });
    expect(onSelect).toHaveBeenCalledWith('custom');
  });

  it('supports compact MetricSummaryGrid density and custom data attributes without meta or sparklines', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <MetricSummaryGrid
          density="compact"
          columns={2}
          cards={[
            {
              label: 'Last Active',
              value: '08/26 17:44',
              valueTitle: '2026-08-26 17:44:00',
              meta: '7d monitoring',
              icon: 'trend',
              accent: 'blue',
              dataAttributes: { 'data-overview-metric-key': 'lastSeenMs' },
            },
          ]}
        />
      );
    });

    const grid = renderer.root.findByProps({ 'data-metric-summary-density': 'compact' });
    expect(grid.findAllByProps({ 'data-metric-summary-meta': 'true' })).toHaveLength(0);
    expect(grid.findAllByProps({ 'data-metric-summary-chart': 'true' })).toHaveLength(0);
    expect(grid.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
    const metric = grid.findByProps({ 'data-overview-metric-key': 'lastSeenMs' });
    expect(metric.findByType('strong').props.title).toBe('2026-08-26 17:44:00');
  });

  it('renders AppFooter with repository, version release, and author links', () => {
    let releaseTree!: ReactTestRenderer;
    act(() => {
      releaseTree = create(<AppFooter version="2.0.0" />);
    });
    const releaseLinks = releaseTree.root.findAllByType('a');
    expect(releaseLinks).toHaveLength(3);
    expect(releaseLinks[0].props.href).toBe(APP_REPOSITORY_URL);
    expect(releaseLinks[1].props.href).toBe(`${APP_RELEASES_URL}/tag/v2.0.0`);
    expect(releaseLinks[1].children).toEqual(['v2.0.0']);
    expect(releaseLinks[2].props.href).toBe(APP_AUTHOR_URL);
    for (const link of releaseLinks) {
      expect(link.props.target).toBe('_blank');
      expect(link.props.rel).toBe('noopener noreferrer');
    }

    let devTree!: ReactTestRenderer;
    act(() => {
      devTree = create(<AppFooter version="2.0.0-dev.542e83fb" />);
    });
    const devVersionLink = devTree.root.findAllByType('a')[1];
    expect(devVersionLink.children).toEqual(['v2.0.0']);
    expect(devVersionLink.props.href).toBe(APP_RELEASES_URL);
    expect(devVersionLink.props.title).toBe('v2.0.0-dev.542e83fb');
  });

  it('renders WorkspaceListPanel header meta, view switcher, empty fallback, and default flush footer', () => {
    const onPageChange = vi.fn();
    const onModeChange = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <WorkspaceListPanel
          testId="list-panel"
          headerTestId="list-header"
          summary="共 10 条"
          hint="批量提示"
          selectionActive
          selectionBadge="已选 2 项"
          selectionTools={<button type="button">全选</button>}
          headerActions={<button type="button">批量选择</button>}
          viewMode={{
            value: 'table',
            onChange: onModeChange,
            tableLabel: '表格',
            cardsLabel: '卡片',
            ariaLabel: '视图切换',
            tableTestId: 'view-table',
            cardsTestId: 'view-cards',
          }}
          empty
          emptyFallback={<div data-testid="empty-box">暂无数据</div>}
          pagination={{
            currentPage: 1,
            totalPages: 2,
            infoText: '第 1 / 2 页',
            prevLabel: '上一页',
            nextLabel: '下一页',
            onPageChange,
          }}
        >
          <div data-testid="list-body">rows</div>
        </WorkspaceListPanel>
      );
    });

    expect(renderer.root.findAllByProps({ 'data-testid': 'list-header' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'empty-box' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'list-body' })).toHaveLength(0);

    const footer = renderer.root.findByType(PaginationFooter);
    expect(footer.props.variant).toBe('flush');
    expect(footer.props.showPageJump).toBe(true);

    const tabs = renderer.root.findByType(SegmentedTabs);
    act(() => {
      tabs.props.onChange?.('grid');
    });
    expect(onModeChange).toHaveBeenCalledWith('grid');

    act(() => renderer.unmount());
  });
});
