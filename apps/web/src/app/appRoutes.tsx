import type { RouteObject } from 'react-router-dom';
import { MainLayout } from '@/components/layout/MainLayout';
import { LoginPage } from '@/pages/LoginPage';
import { ProtectedRoute } from '@/router/ProtectedRoute';
import { RootShell } from './RootShell';

const appRoutes: RouteObject[] = [
  {
    element: <RootShell />,
    children: [
      { path: '/login', element: <LoginPage /> },
      {
        path: '/*',
        element: (
          <ProtectedRoute>
            <MainLayout />
          </ProtectedRoute>
        ),
      },
    ],
  },
];

export const createAppRoutes = (): RouteObject[] => appRoutes;
