import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { readRoutingContext } from './routingContext';
import { BOOTSTRAP_UI, consumeHandoff, createBootstrapTransport } from './bootstrapTransport';
import './carrier.css';

// Non-product carrier only: no Console layout, Bootstrap workflow or login UI.
const context = readRoutingContext(document);
const root = createRoot(document.getElementById('root')!);
if (window.location.pathname === BOOTSTRAP_UI) {
  const handoff = consumeHandoff(window.location, window.history);
  const transport = createBootstrapTransport();
  root.render(<p>CPAMP technical Bootstrap carrier</p>);
  if (handoff) {
    void transport.start(handoff).then(
      () => root.render(<p>Fresh Bootstrap authority established</p>),
      () => root.render(<p>Fresh Bootstrap authority unavailable</p>)
    );
  }
} else if (!context) {
  root.render(<p>Routing authority unavailable</p>);
} else {
  const router = createBrowserRouter(
    [
      { path: '/', element: <p>CPAMP technical carrier — no product design authority</p> },
      {
        path: '/technical-route',
        hydrateFallbackElement: <></>,
        lazy: async () => {
          const { ConsoleCarrier } = await import('./ConsoleCarrier');
          return { Component: ConsoleCarrier };
        },
      },
    ],
    { basename: context.basePath }
  );
  root.render(<RouterProvider router={router} />);
}
