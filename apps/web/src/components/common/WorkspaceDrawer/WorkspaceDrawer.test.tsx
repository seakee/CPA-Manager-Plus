import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import {
  DrawerActionFooter,
  DrawerBodyShell,
  DrawerCopyableMeta,
  DrawerDangerButton,
  DrawerDefinitionList,
  DrawerDlRow,
  DrawerIdentityHeader,
  DrawerNotice,
  DrawerSectionCard,
  DrawerStatusPill,
  DrawerTabBar,
  DrawerTabPanel,
  DrawerViewport,
} from './WorkspaceDrawer';

const extractText = (node: unknown): string => {
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(extractText).join('');
  if (node && typeof node === 'object' && 'children' in node) {
    return extractText((node as { children?: unknown }).children);
  }
  return '';
};

describe('WorkspaceDrawer shared primitives', () => {
  it('renders identity header with auto-separated meta items, status pill, and copyable meta', () => {
    const onCopy = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DrawerIdentityHeader
          icon={<span data-testid="icon">I</span>}
          title="Claude Official"
          metaItems={[
            'Claude',
            null,
            <DrawerStatusPill key="status" tone="success">
              Enabled
            </DrawerStatusPill>,
            '',
            <DrawerCopyableMeta key="host" onCopy={onCopy} testId="copy-host">
              api.anthropic.com
            </DrawerCopyableMeta>,
          ]}
        />
      );
    });

    const text = extractText(renderer.toJSON());
    expect(text).toBe('IClaude OfficialClaude·Enabled·api.anthropic.com');

    act(() => {
      renderer.root.findByProps({ 'data-testid': 'copy-host' }).props.onClick();
    });
    expect(onCopy).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('supports keyboard ArrowRight/ArrowLeft/Home/End navigation, count badge, and dirty dot in DrawerTabBar', () => {
    const onChange = vi.fn();
    const scrollEl = { scrollTop: 120 } as HTMLElement;
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DrawerBodyShell
          tabs={
            <DrawerTabBar
              ariaLabel="Detail tabs"
              idBase="test"
              activeTab="overview"
              onChange={onChange}
              scrollContainerRef={{ current: scrollEl }}
              items={[
                { id: 'overview', label: 'Overview' },
                { id: 'config', label: 'Config', dirty: true },
                { id: 'models', label: 'Models', count: 4 },
              ]}
            />
          }
        >
          <DrawerViewport>
            <DrawerTabPanel id="test-panel-overview" labelledBy="test-tab-overview">
              <DrawerNotice tone="warning" title="Read-only">
                Shared credential notice
              </DrawerNotice>
              <DrawerSectionCard title="Routing">
                <DrawerDefinitionList>
                  <DrawerDlRow label="Base URL" mono>
                    https://api.example.com
                  </DrawerDlRow>
                </DrawerDefinitionList>
              </DrawerSectionCard>
            </DrawerTabPanel>
          </DrawerViewport>
        </DrawerBodyShell>
      );
    });

    const tabs = renderer.root.findAllByProps({ role: 'tab' });
    expect(tabs).toHaveLength(3);
    expect(tabs[0].props['aria-selected']).toBe(true);
    expect(tabs[0].props.tabIndex).toBe(0);
    expect(tabs[1].props.tabIndex).toBe(-1);
    expect(extractText(tabs[2])).toContain('Models4');

    act(() => {
      tabs[0].props.onKeyDown({ key: 'ArrowRight', preventDefault: vi.fn() });
    });
    expect(onChange).toHaveBeenCalledWith('config');
    expect(scrollEl.scrollTop).toBe(0);

    act(() => {
      tabs[1].props.onKeyDown({ key: 'End', preventDefault: vi.fn() });
    });
    expect(onChange).toHaveBeenCalledWith('models');
    act(() => renderer.unmount());
  });

  it('renders single-authority split footer with left lifecycle and right CTA slots', () => {
    const onDelete = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DrawerActionFooter
          testId="footer"
          left={<DrawerDangerButton onClick={onDelete}>Delete</DrawerDangerButton>}
          right={<button type="button">Save</button>}
        />
      );
    });
    expect(extractText(renderer.toJSON())).toBe('DeleteSave');
    act(() => {
      renderer.root.findByType(DrawerDangerButton).props.onClick();
    });
    expect(onDelete).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });
});
