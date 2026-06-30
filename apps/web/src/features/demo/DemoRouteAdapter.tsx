import { ReactNode, useContext, useMemo } from 'react';
import {
  UNSAFE_NavigationContext,
  parsePath,
  type NavigateOptions,
  type Navigator,
  type To,
} from 'react-router-dom';
import { prefixRouteBase } from './demoMode';

interface DemoRouteAdapterProps {
  children: ReactNode;
  routeBase: string;
}

const normalizeRouteBase = (routeBase: string) => routeBase.replace(/\/+$/g, '') || '/';

const shouldPrefixPathname = (pathname: string | undefined, routeBase: string) => {
  if (!pathname || !pathname.startsWith('/')) return false;
  const base = normalizeRouteBase(routeBase);
  if (!base || base === '/') return false;
  return pathname !== base && !pathname.startsWith(`${base}/`);
};

const prefixTo = (to: To, routeBase: string): To => {
  if (typeof to === 'string') {
    const path = parsePath(to);
    if (!shouldPrefixPathname(path.pathname, routeBase)) return to;
    return `${prefixRouteBase(path.pathname || '/', routeBase)}${path.search || ''}${path.hash || ''}`;
  }

  if (!shouldPrefixPathname(to.pathname, routeBase)) return to;
  return {
    ...to,
    pathname: prefixRouteBase(to.pathname || '/', routeBase),
  };
};

export function DemoRouteAdapter({ children, routeBase }: DemoRouteAdapterProps) {
  const navigationContext = useContext(UNSAFE_NavigationContext);

  const value = useMemo(() => {
    const encodeLocation = navigationContext.navigator.encodeLocation;
    const navigator: Navigator = {
      ...navigationContext.navigator,
      createHref: (to) => navigationContext.navigator.createHref(prefixTo(to, routeBase)),
      encodeLocation: encodeLocation
        ? (to) => encodeLocation(prefixTo(to, routeBase))
        : undefined,
      push: (to: To, state?: unknown, opts?: NavigateOptions) => {
        navigationContext.navigator.push(prefixTo(to, routeBase), state, opts);
      },
      replace: (to: To, state?: unknown, opts?: NavigateOptions) => {
        navigationContext.navigator.replace(prefixTo(to, routeBase), state, opts);
      },
    };

    return {
      ...navigationContext,
      navigator,
    };
  }, [navigationContext, routeBase]);

  return (
    <UNSAFE_NavigationContext.Provider value={value}>
      {children}
    </UNSAFE_NavigationContext.Provider>
  );
}
