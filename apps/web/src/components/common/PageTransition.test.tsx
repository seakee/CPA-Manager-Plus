import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PageTransition } from './PageTransition';

const mocks = vi.hoisted(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    location: { pathname: '/usage-maintenance', key: 'preserved', search: '', hash: '', state: null },
  };
});

vi.mock('react-router-dom', () => ({ useLocation: () => mocks.location }));
vi.mock('motion/mini', () => ({ animate: vi.fn() }));

describe('PageTransition location identity', () => {
  let renderer: ReactTestRenderer | undefined;
  const render = () => (
    <PageTransition
      getTransitionVariant={() => 'none'}
      render={(location) => <p>{location.pathname}</p>}
    />
  );

  beforeEach(() => {
    mocks.location = { pathname: '/usage-maintenance', key: 'preserved', search: '', hash: '', state: null };
  });

  afterEach(async () => {
    await act(async () => renderer?.unmount());
    renderer = undefined;
  });

  it('updates a hash navigation when the browser preserves the history state key', async () => {
    await act(async () => { renderer = create(render()); });
    expect(renderer!.root.findByType('p').children).toEqual(['/usage-maintenance']);

    mocks.location = { ...mocks.location, pathname: '/model-prices' };
    await act(async () => { renderer!.update(render()); });
    expect(renderer!.root.findByType('p').children).toEqual(['/model-prices']);

    mocks.location = { ...mocks.location, pathname: '/usage-maintenance' };
    await act(async () => { renderer!.update(render()); });
    expect(renderer!.root.findByType('p').children).toEqual(['/usage-maintenance']);
  });

  it('continues to update ordinary router navigation with a new history key', async () => {
    await act(async () => { renderer = create(render()); });
    mocks.location = { ...mocks.location, pathname: '/model-prices', key: 'next' };
    await act(async () => { renderer!.update(render()); });
    expect(renderer!.root.findByType('p').children).toEqual(['/model-prices']);
  });
});
