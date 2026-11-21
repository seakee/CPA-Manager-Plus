import { renderToStaticMarkup } from 'react-dom/server';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import {
  SegmentedTabs,
  type SegmentedTabItem,
  type SegmentedTabsLinkProps,
} from './SegmentedTabs';

const baseItems: ReadonlyArray<SegmentedTabItem<'visual' | 'source' | 'manager'>> = [
  { id: 'visual', label: 'Visual' },
  { id: 'source', label: 'Source' },
  { id: 'manager', label: 'Manager', disabled: true },
];

function TestLink({ to, children, ...props }: SegmentedTabsLinkProps) {
  return (
    <a href={to} {...props}>
      {children}
    </a>
  );
}

describe('SegmentedTabs', () => {
  it('renders one tab per item', () => {
    const markup = renderToStaticMarkup(
      <SegmentedTabs
        items={baseItems}
        activeTab="visual"
        onChange={() => {}}
        ariaLabel="Editor tabs"
      />
    );

    expect(markup.match(/role="tab"/g) ?? []).toHaveLength(3);
    expect(markup).toContain('role="tablist"');
    expect(markup).toContain('aria-label="Editor tabs"');
  });

  it('marks the active tab with aria-selected and tabIndex', () => {
    const markup = renderToStaticMarkup(
      <SegmentedTabs
        items={baseItems}
        activeTab="source"
        onChange={() => {}}
        ariaLabel="Editor tabs"
      />
    );

    expect(markup).toContain('id="segmented-tabs-source" aria-selected="true" tabindex="0"');
    expect(markup).toContain('id="segmented-tabs-visual" aria-selected="false" tabindex="-1"');
  });

  it('calls onChange when clicking an enabled inactive tab', () => {
    const changes: string[] = [];
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <SegmentedTabs
          items={baseItems}
          activeTab="visual"
          onChange={(tab) => changes.push(tab)}
          ariaLabel="Editor tabs"
        />
      );
    });

    const tabs = renderer!.root.findAllByProps({ role: 'tab' });

    act(() => {
      tabs[1].props.onClick({ preventDefault: () => {} });
    });

    expect(changes).toEqual(['source']);
  });

  it('does not call onChange for disabled tabs', () => {
    const changes: string[] = [];
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <SegmentedTabs
          items={baseItems}
          activeTab="visual"
          onChange={(tab) => changes.push(tab)}
          ariaLabel="Editor tabs"
        />
      );
    });

    const tabs = renderer!.root.findAllByProps({ role: 'tab' });

    act(() => {
      tabs[2].props.onClick({ preventDefault: () => {} });
    });

    expect(changes).toEqual([]);
  });

  it('renders link tabs when a link component is provided', () => {
    const markup = renderToStaticMarkup(
      <SegmentedTabs
        items={[
          { id: 'local', label: 'Local', to: '/codex-inspection' },
          { id: 'server', label: 'Server', to: '/codex-inspection/server' },
        ]}
        activeTab="server"
        ariaLabel="Inspection mode"
        linkComponent={TestLink}
      />
    );

    expect(markup).toContain('href="/codex-inspection/server"');
    expect(markup).toContain('aria-selected="true"');
  });

  it('exposes variant and size attributes with joined and sm defaults overridden', () => {
    const defaultMarkup = renderToStaticMarkup(
      <SegmentedTabs
        items={baseItems}
        activeTab="visual"
        onChange={() => {}}
        ariaLabel="Editor tabs"
      />
    );
    expect(defaultMarkup).toContain('data-variant="default"');
    expect(defaultMarkup).toContain('data-size="md"');

    const joinedSmMarkup = renderToStaticMarkup(
      <SegmentedTabs
        items={baseItems}
        activeTab="visual"
        onChange={() => {}}
        ariaLabel="Compact joined tabs"
        variant="joined"
        size="sm"
      />
    );
    expect(joinedSmMarkup).toContain('data-variant="joined"');
    expect(joinedSmMarkup).toContain('data-size="sm"');
  });
});
