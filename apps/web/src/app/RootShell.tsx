import { Outlet } from 'react-router-dom';
import { AppBackground } from '@/components/common/AppBackground';
import { ConfirmationModal } from '@/components/common/ConfirmationModal';
import { NotificationContainer } from '@/components/common/NotificationContainer';
import { ManagerUpdates } from '@/features/system/ManagerUpdates';

export function RootShell() {
  return (
    <>
      <AppBackground />
      <div className="app-content">
        <NotificationContainer />
        <ConfirmationModal />
        <ManagerUpdates>
          <Outlet />
        </ManagerUpdates>
      </div>
    </>
  );
}
