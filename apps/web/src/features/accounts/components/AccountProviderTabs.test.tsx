import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { AccountProviderTabs } from './AccountProviderTabs';

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: (key: string) =>
      ({
        'accounts.filter_all': 'All',
        'accounts.provider_filter': 'Platform filter',
        'auth_files.filter_claude': 'Claude',
        'auth_files.filter_codex': 'Codex',
        'auth_files.filter_devin': 'Devin',
        'auth_files.filter_xai': 'xAI',
      })[key] ?? key,
  }),
}));

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object' && 'props' in value) {
    return readText((value as { props: { children?: unknown } }).props.children);
  }
  return '';
};

const findTab = (renderer: ReactTestRenderer, provider: string) =>
  renderer.root.findByProps({ id: `accounts-provider-filter-${provider}` });

describe('AccountProviderTabs', () => {
  it('renders stable provider counts from all credential rows and switches platform filters', () => {
    const changes: string[] = [];
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <AccountProviderTabs
          rows={[{ provider: 'xai' }, { provider: 'codex' }, { provider: 'devin' }, { provider: 'xai' }]}
          value="all"
          onChange={(provider) => changes.push(provider)}
          resolvedTheme="light"
        />
      );
    });

    const tabs = renderer!.root.findAllByProps({ role: 'tab' });
    expect(tabs.map((tab) => readText(tab.props.children))).toEqual([
      'All4',
      'Codex1',
      'Devin1',
      'xAI2',
    ]);
    expect(renderer!.root.findByProps({ role: 'tablist' }).props['aria-label']).toBe(
      'Platform filter'
    );
    expect(findTab(renderer!, 'all').props['aria-selected']).toBe(true);

    act(() => {
      findTab(renderer!, 'devin').props.onClick({ preventDefault: () => {} });
    });

    act(() => {
      findTab(renderer!, 'xai').props.onClick({ preventDefault: () => {} });
    });

    expect(changes).toEqual(['devin', 'xai']);
  });

  it('keeps an absent deep-linked platform visible with a zero count', () => {
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <AccountProviderTabs
          rows={[{ provider: 'codex' }]}
          value="claude"
          onChange={() => {}}
          resolvedTheme="dark"
        />
      );
    });

    const selectedTab = findTab(renderer!, 'claude');
    expect(selectedTab.props['aria-selected']).toBe(true);
    expect(readText(selectedTab.props.children)).toBe('Claude0');
  });
});
