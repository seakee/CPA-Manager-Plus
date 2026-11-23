import { useEffect, useState, type ReactElement } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuthStore } from '@/stores';
import { SplashScreen } from '@/components/common/SplashScreen';
import { isUsageServiceId, usageServiceApi } from '@/services/api/usageService';
import { detectApiBaseFromLocation } from '@/utils/connection';

export function ProtectedRoute({ children }: { children: ReactElement }) {
  const location = useLocation();
  const navigate = useNavigate();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const managementKey = useAuthStore((state) => state.managementKey);
  const apiBase = useAuthStore((state) => state.apiBase);
  const restoreSession = useAuthStore((state) => state.restoreSession);
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    const tryRestore = async () => {
      if (!isAuthenticated && managementKey && apiBase) {
        setChecking(true);
        try {
          const detectedBase = detectApiBaseFromLocation();
          let detectedUsageService = false;
          try {
            const info = await usageServiceApi.getInfo(detectedBase);
            detectedUsageService = isUsageServiceId(info.service);
          } catch {
            detectedUsageService = false;
          }
          const hostedManagementPage =
            typeof window !== 'undefined' &&
            /\/management\.html$/i.test(window.location.pathname);
          const result = await restoreSession({
            expectedMode: detectedUsageService ? 'manager_embedded' : 'external_panel',
            expectedPanelBase:
              detectedUsageService || hostedManagementPage ? detectedBase : undefined,
          });
          if (result && result.recoveryMode === 'manager_config') {
            localStorage.setItem('config-management:tab', 'manager');
            navigate('/config', { replace: true });
          }
        } finally {
          setChecking(false);
        }
      }
    };
    tryRestore();
  }, [apiBase, isAuthenticated, managementKey, navigate, restoreSession]);

  if (checking) {
    return <SplashScreen />;
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location }} />;
  }

  return children;
}
