import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import '@/i18n';
import { OAuthExcludedCard } from './OAuthExcludedCard';
import { OAuthModelAliasCard } from './OAuthModelAliasCard';

const noop = () => {};
const noopAsync = async () => {};

describe('OAuth configuration load guards', () => {
  it('disables excluded-model writes and exposes retry after a load failure', () => {
    const markup = renderToStaticMarkup(
      createElement(OAuthExcludedCard, {
        disableControls: false,
        loadState: 'error',
        excluded: {},
        onRetry: noop,
        onAdd: noop,
        onEdit: noop,
        onDelete: noop,
      })
    );

    expect(markup).toContain('disabled=""');
    expect(markup).toContain('empty-action');
  });

  it('disables model-alias writes and exposes retry after a load failure', () => {
    const markup = renderToStaticMarkup(
      createElement(OAuthModelAliasCard, {
        disableControls: false,
        viewMode: 'list',
        onViewModeChange: noop,
        onRetry: noop,
        onAdd: noop,
        onEditProvider: noop,
        onDeleteProvider: noop,
        loadState: 'error',
        modelAlias: {},
        allProviderModels: {},
        onUpdate: noopAsync,
        onDeleteLink: noop,
        onToggleFork: noopAsync,
        onRenameAlias: noopAsync,
        onDeleteAlias: noop,
      })
    );

    expect(markup).toContain('disabled=""');
    expect(markup).toContain('empty-action');
  });
});
