import { createRoot } from 'react-dom/client';
import { lazy, Suspense, type ReactNode } from 'react';
import { AppBackground } from '../components/common/AppBackground';
import { isInitialRoutingContext, readRoutingContext } from './routingContext';
import { activateFreshLanguage } from './freshLanguage';
import { BOOTSTRAP_UI, consumeHandoff, createBootstrapTransport } from './bootstrapTransport';
import { FreshBootstrapPage } from './FreshBootstrapPage';
import '../styles/global.scss';
import './carrier.css';

// Apply the language the Console already persists before anything renders, so
// the Bootstrap entry never flashes the browser default first.
activateFreshLanguage();
const context = readRoutingContext(document);
const root = createRoot(document.getElementById('root')!);
const FreshConsolePage = lazy(() =>
  import('./FreshConsolePage').then((module) => ({ default: module.FreshConsolePage }))
);
const ConsoleCarrier = lazy(() =>
  import('./ConsoleCarrier').then((module) => ({ default: module.ConsoleCarrier }))
);
const render = (page: ReactNode) =>
  root.render(
    <>
      <AppBackground />
      <div className="app-content">{page}</div>
    </>
  );
if (window.location.pathname === BOOTSTRAP_UI) {
  let transport = createBootstrapTransport();
  let mount = 0;
  const openHandoff = () => {
    const handoff = consumeHandoff(window.location, window.history);
    transport.clear();
    transport = createBootstrapTransport();
    render(<FreshBootstrapPage key={++mount} handoff={handoff} transport={transport} />);
  };
  // Pasting a host-issued link into this already-open page may navigate only
  // the fragment. Retire the previous in-memory authority before remounting.
  window.addEventListener('hashchange', openHandoff);
  openHandoff();
} else if (isInitialRoutingContext(document)) {
  // An uncommitted initial route is a verification candidate, never a handoff
  // consumer or an active Console. Strip any fragment and show Bootstrap status.
  if (window.location.hash) {
    window.history.replaceState(null, '', window.location.pathname + window.location.search);
  }
  render(<FreshBootstrapPage handoff={null} transport={createBootstrapTransport()} />);
} else if (!context) {
  render(<p>Routing authority unavailable</p>);
} else {
  // The Fresh shell owns the host route prefix and the existing CPAMP pages
  // mount their own BrowserRouter at the authenticated seam. Keeping this
  // entry point unwrapped avoids a second data-router basename competing with
  // MainLayout's established `/console/...` navigation while preserving the
  // technical carrier as a lazy, directly addressable boundary.
  const basePath = context.basePath.replace(/\/+$/, '');
  const isTechnicalRoute = window.location.pathname === `${basePath}/technical-route`;
  render(
    <Suspense fallback={<></>}>
      {isTechnicalRoute ? <ConsoleCarrier /> : <FreshConsolePage context={context} />}
    </Suspense>
  );
}
