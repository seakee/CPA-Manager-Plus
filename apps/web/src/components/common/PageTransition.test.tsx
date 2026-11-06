import { useEffect } from 'react';
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
vi.mock('motion/mini', () => ({
  animate: vi.fn(() => ({ finished: Promise.resolve(), stop: vi.fn() })),
}));

describe('PageTransition location identity', () => {
  let renderer: ReactTestRenderer | undefined;
  const render = () => (
    <PageTransition
      getTransitionVariant={() => 'none'}
      render={(location) => <p>{location.pathname}</p>}
    />
  );

  beforeEach(() => {
    mocks.location = {
      pathname: '/usage-maintenance',
      key: 'preserved',
      search: '',
      hash: '',
      state: null,
    };
  });

  afterEach(async () => {
    await act(async () => renderer?.unmount());
    renderer = undefined;
  });

  it('continues to update ordinary router navigation with a new history key', async () => {
    await act(async () => {
      renderer = create(render());
    });
    mocks.location = { ...mocks.location, pathname: '/model-prices', key: 'next' };
    await act(async () => {
      renderer!.update(render());
    });
    expect(renderer!.root.findByType('p').children).toEqual(['/model-prices']);
  });

  it('updates search-only navigation in place without remounting the page', async () => {
    let mounts = 0;
    const Page = ({ search }: { search: string }) => {
      useEffect(() => {
        mounts += 1;
      }, []);
      return <p>{search}</p>;
    };
    const renderSearch = () => (
      <PageTransition
        getTransitionVariant={() => 'none'}
        render={(location) => <Page search={location.search} />}
      />
    );

    await act(async () => {
      renderer = create(renderSearch());
    });
    mocks.location = { ...mocks.location, search: '?provider=codex', key: 'replace-1' };
    await act(async () => {
      renderer!.update(renderSearch());
    });
    expect(renderer!.root.findByType('p').children).toEqual(['?provider=codex']);

    // Returning to the entry URL must reach the page too, not just differ from it.
    mocks.location = { ...mocks.location, search: '', key: 'replace-2' };
    await act(async () => {
      renderer!.update(renderSearch());
    });
    expect(renderer!.root.findByType('p').children).toEqual([]);
    expect(mounts).toBe(1);
  });

  it('lets the exiting page keep its latest search during a page transition', async () => {
    // An exiting page that sees its entry URL again would write it back and
    // hijack the navigation (e.g. useSearchParams state sync).
    const renderAnimated = () => (
      <PageTransition
        getTransitionVariant={() => 'vertical'}
        render={(location) => <p>{`${location.pathname}${location.search}`}</p>}
      />
    );

    await act(async () => {
      renderer = create(renderAnimated());
    });
    mocks.location = { ...mocks.location, search: '?time_range=7d', key: 'replace-1' };
    await act(async () => {
      renderer!.update(renderAnimated());
    });
    mocks.location = { ...mocks.location, pathname: '/logs', search: '', key: 'push-1' };
    await act(async () => {
      renderer!.update(renderAnimated());
    });

    expect(renderer!.root.findAllByType('p').map((node) => node.children[0])).toEqual([
      '/usage-maintenance?time_range=7d',
      '/logs',
    ]);
  });
});
