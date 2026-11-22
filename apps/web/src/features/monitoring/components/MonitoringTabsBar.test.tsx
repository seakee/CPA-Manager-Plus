import { renderToStaticMarkup } from 'react-dom/server';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import styles from '@/features/monitoring/MonitoringCenterPage.module.scss';
import { MonitoringTabsBar } from './MonitoringTabsBar';

describe('MonitoringTabsBar', () => {
  const baseTabs = [
    { id: 'accounts' as const, label: 'Accounts', icon: 'accounts' as const, badge: 12 },
    { id: 'apiKeys' as const, label: 'Client Keys', icon: 'apiKeys' as const, badge: 27 },
    {
      id: 'realtime' as const,
      label: 'Realtime',
      icon: 'realtime' as const,
      badge: 3,
      badgeTone: 'failure' as const,
      badgeTitle: '3 failed / 1200 total',
    },
  ];

  it('renders one tab button per entry with role=tab', () => {
    const markup = renderToStaticMarkup(
      <MonitoringTabsBar
        tabs={baseTabs}
        activeTab="accounts"
        onChange={() => {}}
        ariaLabel="Data view tabs"
      />
    );

    const matches = markup.match(/role="tab"/g) ?? [];
    expect(matches).toHaveLength(3);
    expect(markup).toContain('aria-label="Data view tabs"');
    expect(markup).toContain('role="tablist"');
  });

  it('marks the active tab with aria-selected=true and tabIndex=0', () => {
    const markup = renderToStaticMarkup(
      <MonitoringTabsBar tabs={baseTabs} activeTab="apiKeys" onChange={() => {}} ariaLabel="Tabs" />
    );

    expect(markup).toContain(
      'id="monitoring-data-tabs-apiKeys" aria-selected="true" aria-controls="monitoring-data-tabs-apiKeys-panel" tabindex="0"'
    );
    expect(markup).toContain(
      'id="monitoring-data-tabs-accounts" aria-selected="false" aria-controls="monitoring-data-tabs-accounts-panel" tabindex="-1"'
    );
  });

  it('renders failure-toned badge for the realtime tab', () => {
    const markup = renderToStaticMarkup(
      <MonitoringTabsBar
        tabs={baseTabs}
        activeTab="accounts"
        onChange={() => {}}
        ariaLabel="Tabs"
      />
    );

    expect(markup).toContain(styles.tabBadgeFailure);
    expect(markup).toContain('title="3 failed / 1200 total"');
  });

  it('skips badge rendering when badge is null', () => {
    const tabs = [
      { id: 'accounts' as const, label: 'Accounts', badge: null },
      { id: 'apiKeys' as const, label: 'Client Keys', badge: 5 },
    ];
    const markup = renderToStaticMarkup(
      <MonitoringTabsBar tabs={tabs} activeTab="accounts" onChange={() => {}} ariaLabel="Tabs" />
    );

    const badgeMatches = markup.match(new RegExp(styles.tabBadge, 'g')) ?? [];
    expect(badgeMatches).toHaveLength(1);
  });

  it('renders joined SegmentedTabs variant when variant=cards', () => {
    const markup = renderToStaticMarkup(
      <MonitoringTabsBar
        tabs={baseTabs}
        activeTab="accounts"
        onChange={() => {}}
        ariaLabel="Tabs"
        variant="cards"
      />
    );

    expect(markup).toContain('data-variant="joined"');
    expect(markup).toContain('aria-selected="true"');
    expect(markup).toContain(styles.tabIcon);
  });

  it('moves selection with arrow keys', () => {
    const changes: string[] = [];
    let prevented = false;
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <MonitoringTabsBar
          tabs={baseTabs}
          activeTab="accounts"
          onChange={(tab) => changes.push(tab)}
          ariaLabel="Tabs"
        />
      );
    });

    const tabs = renderer!.root.findAllByProps({ role: 'tab' });

    act(() => {
      tabs[0].props.onKeyDown({
        key: 'ArrowRight',
        preventDefault: () => {
          prevented = true;
        },
      });
    });

    expect(prevented).toBe(true);
    expect(changes).toEqual(['apiKeys']);
  });
});
