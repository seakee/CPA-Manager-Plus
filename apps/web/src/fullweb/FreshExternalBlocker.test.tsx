import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshExternalBlocker } from './FreshExternalBlocker';
import { blockerOf } from './externalState';
import { externalWorkspaceText as x } from '../i18n/freshExternal';

// The Modal is a portal in the browser and an inline block in these tests, so a
// dialog's own content is assertable without a DOM.
vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));

// The rendered text of one subtree: a renderer node's children are elements as
// well as strings, so the leaves are collected rather than stringified (the React
// tree is circular).
function textOf(renderer: ReactTestRenderer, testId: string): string {
  const node = renderer.root.find((candidate) => candidate.props['data-testid'] === testId);
  return node
    .findAll((candidate) => typeof candidate.type === 'string')
    .flatMap((candidate) => candidate.children)
    .filter((child): child is string => typeof child === 'string')
    .join(' ');
}

describe('External blocker states', () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });
  afterEach(() => vi.unstubAllGlobals());

  it('maps each typed wire code onto its own state', () => {
    expect(blockerOf('external_management_key_rejected')).toEqual({ kind: 'key_rejected' });
    expect(blockerOf('external_target_moved')).toEqual({ kind: 'target_moved' });
    expect(blockerOf('external_cpa_version_unsupported')).toEqual({
      kind: 'version_unsupported',
    });
    // The legacy bridge's replaced-write code has no banner: no reachable page
    // performs a write the bridge owns, so it stays a raw wire answer.
    expect(blockerOf('external_legacy_write_replaced')).toBeNull();
    expect(blockerOf('external_rebind_refused')).toEqual({ kind: 'rebind_refused' });
    expect(blockerOf('external_rebind_stale_generation')).toEqual({ kind: 'rebind_stale' });
    expect(blockerOf('external_cpa_authority_unavailable')).toEqual({
      kind: 'authority_unavailable',
    });
    // An unknown code is not a state: the page keeps its own contract rather than
    // guessing a cause.
    expect(blockerOf('boom')).toBeNull();
    expect(blockerOf(undefined)).toBeNull();
  });

  it('offers a rebind for a refused key and nothing to write for a moved target', async () => {
    const rebind = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshExternalBlocker state={{ kind: 'key_rejected' }} onRebind={rebind} />);
    });
    const button = renderer.root.find(
      (node) => node.type === 'button' && node.props['data-testid'] === 'external-blocker-rebind'
    );
    await act(async () => button.props.onClick());
    expect(rebind).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer.update(<FreshExternalBlocker state={{ kind: 'target_moved' }} onRebind={rebind} />);
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'button' && node.props['data-testid'] === 'external-blocker-rebind'
      )
    ).toHaveLength(0);
    expect(rebind).toHaveBeenCalledTimes(1);
  });

  it('shows the version and its support class when the version is unsupported', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalBlocker
          state={{ kind: 'version_unsupported', version: 'v7.0.12', support: 'unsupported' }}
        />
      );
    });
    const text = textOf(renderer, 'external-blocker-version');
    expect(text).toContain('v7.0.12');
    // The support class is a real translation, never the raw wire token.
    expect(text).not.toContain('unsupported');
  });

  // The server's `detail` is English. It must never be the sentence the page
  // speaks in another locale: the instruction is localized and the diagnostic is
  // offered behind its own disclosure.
  // The server's `detail` is English. It must never be the sentence the page
  // speaks in another locale: the instruction is localized and the diagnostic is
  // offered behind its own disclosure.
  // The server's `detail` is English. It must never be the sentence the page
  // speaks: the instruction is the localized table's, and the diagnostic is
  // offered behind its own disclosure.
  it('keeps an English detail out of a localized instruction', async () => {
    const detail = 'the CPA replaced this key or is refusing this client';
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshExternalBlocker state={{ kind: 'key_rejected', detail }} />);
    });
    const instructions = () =>
      renderer.root
        .findAll((node) => node.type === 'p')
        .map((node) =>
          node.children.filter((child): child is string => typeof child === 'string').join('')
        )
        .filter((text) => text !== '');
    // The first paragraph is the instruction; it is the locale's own sentence and
    // never the English detail.
    expect(instructions()[0]).toBe(x('blockerKeyRejectedHelp'));
    expect(instructions()[0]).not.toContain(detail);
    expect(textOf(renderer, 'external-blocker-detail')).toContain(detail);
  });

  it('offers a reload only for a stale generation', async () => {
    const reload = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshExternalBlocker state={{ kind: 'rebind_stale' }} onReload={reload} />);
    });
    const button = renderer.root.find(
      (node) => node.type === 'button' && node.props['data-testid'] === 'external-blocker-reload'
    );
    await act(async () => button.props.onClick());
    expect(reload).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer.update(<FreshExternalBlocker state={{ kind: 'key_rejected' }} onReload={reload} />);
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'button' && node.props['data-testid'] === 'external-blocker-reload'
      )
    ).toHaveLength(0);
  });
});
