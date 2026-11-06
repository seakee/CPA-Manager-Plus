import { act } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { Input } from '@/components/ui/Input';
import { OAuthExcludedCard } from './OAuthExcludedCard';
import { OAuthModelAliasCard } from './OAuthModelAliasCard';

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const values = options
        ? Object.entries(options)
            .filter(([name]) => name !== 'defaultValue')
            .map(([name, value]) => `${name}=${String(value)}`)
            .join(' ')
        : '';
      return values ? `${key} ${values}` : key;
    },
  }),
}));

const noop = () => {};
const noopAsync = async () => {};

const renderText = (renderer: ReactTestRenderer) => JSON.stringify(renderer.toJSON());

describe('OAuth rule cards', () => {
  it.each(['loading', 'error'] as const)(
    'retains rules, including loaded empty snapshots, in %s state',
    (loadState) => {
      let renderer!: ReactTestRenderer;
      const renderCards = (empty: boolean) => (
        <>
          <OAuthExcludedCard
            hasLoaded
            disableControls={false}
            loadState={loadState}
            excluded={empty ? {} : { codex: ['retained-model'] }}
            onRetry={noop}
            onAdd={noop}
            onEdit={noop}
            onDelete={noop}
          />
          <OAuthModelAliasCard
            hasLoaded
            disableControls={false}
            viewMode="list"
            onViewModeChange={noop}
            onRetry={noop}
            onAdd={noop}
            onEditProvider={noop}
            onDeleteProvider={noop}
            loadState={loadState}
            modelAlias={empty ? {} : { codex: [{ name: 'source', alias: 'retained-alias' }] }}
            allProviderModels={{}}
            onUpdate={noopAsync}
            onDeleteLink={noop}
            onToggleFork={noopAsync}
            onRenameAlias={noopAsync}
            onDeleteAlias={noop}
          />
        </>
      );
      act(() => {
        renderer = create(renderCards(false));
      });
      expect(renderText(renderer)).toContain('retained-model');
      expect(renderText(renderer)).toContain('retained-alias');
      if (loadState === 'error')
        expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(2);
      act(() => {
        renderer.update(renderCards(true));
      });
      expect(renderText(renderer)).toContain('oauth_excluded.list_empty_all');
      expect(renderText(renderer)).not.toContain('common.loading');
      act(() => renderer.unmount());
    }
  );

  it('summarizes, filters and expands disabled-model rules without exposing a danger button', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <OAuthExcludedCard
          disableControls={false}
          loadState="ready"
          excluded={{
            codex: ['codex-one', 'codex-two', 'codex-three', 'codex-four'],
            kimi: ['kimi-model'],
          }}
          onRetry={noop}
          onAdd={noop}
          onEdit={noop}
          onDelete={noop}
        />
      );
    });

    expect(renderText(renderer!)).toContain('oauth_excluded.summary providers=2 count=5');
    expect(renderText(renderer!)).not.toContain('codex-four');

    const expandButton = renderer!.root
      .findAllByType('button')
      .find((node) => String(node.props.children).includes('oauth_excluded.expand_models'));
    expect(expandButton).toBeDefined();
    act(() => expandButton!.props.onClick());
    expect(renderText(renderer!)).toContain('codex-four');

    const searchInput = renderer!.root.findByType(Input);
    act(() => searchInput.props.onChange({ target: { value: 'kimi' } }));
    expect(renderText(renderer!)).toContain('kimi-model');
    expect(renderText(renderer!)).not.toContain('codex-one');

    const menus = renderer!.root.findAllByType(DropdownMenu);
    expect(menus).toHaveLength(1);
    expect(menus[0]?.props.items[0]).toMatchObject({
      key: 'delete-rule',
      label: 'oauth_excluded.delete',
      tone: 'danger',
    });

    act(() => renderer!.unmount());
  });

  it('shows every CPA alias semantic and keeps deletion in the provider menu', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <OAuthModelAliasCard
          disableControls={false}
          viewMode="list"
          onViewModeChange={noop}
          onRetry={noop}
          onAdd={noop}
          onEditProvider={noop}
          onDeleteProvider={noop}
          loadState="ready"
          modelAlias={{
            codex: [
              {
                name: 'gpt-5-codex',
                alias: 'team-codex',
                fork: true,
                displayName: 'Team Codex',
                forceMapping: true,
              },
              { name: 'gpt-5-mini', alias: 'team-mini' },
              { name: 'gpt-5-fast', alias: 'team-fast' },
              { name: 'gpt-5-long', alias: 'team-long' },
            ],
          }}
          allProviderModels={{}}
          onUpdate={noopAsync}
          onDeleteLink={noop}
          onToggleFork={noopAsync}
          onRenameAlias={noopAsync}
          onDeleteAlias={noop}
        />
      );
    });

    const initialText = renderText(renderer!);
    expect(initialText).toContain('oauth_model_alias.list_fork_keep');
    expect(initialText).toContain('oauth_model_alias.list_response_rewrite');
    expect(initialText).toContain('oauth_model_alias.list_display_name name=Team Codex');
    expect(initialText).toContain('oauth_model_alias.list_fork_replace');
    expect(initialText).toContain('oauth_model_alias.list_response_passthrough');
    expect(initialText).not.toContain('team-long');

    const expandButton = renderer!.root
      .findAllByType('button')
      .find((node) => String(node.props.children).includes('oauth_model_alias.expand_mappings'));
    expect(expandButton).toBeDefined();
    act(() => expandButton!.props.onClick());
    expect(renderText(renderer!)).toContain('team-long');

    const menu = renderer!.root.findByType(DropdownMenu);
    expect(menu.props.items[0]).toMatchObject({
      key: 'delete-rule',
      label: 'oauth_model_alias.delete',
      tone: 'danger',
    });

    act(() => renderer!.unmount());
  });
});
