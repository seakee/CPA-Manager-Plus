import { useMemo } from 'react';
import { RouterProvider, createHashRouter } from 'react-router-dom';
import { createAppRoutes } from './appRoutes';

export function AppRouter() {
  const router = useMemo(() => createHashRouter(createAppRoutes()), []);
  return <RouterProvider router={router} />;
}
