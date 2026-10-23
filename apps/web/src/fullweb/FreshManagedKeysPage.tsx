import { workspaceText as w } from '../i18n/consoleWorkspace';
import { FreshApiKeys } from './FreshApiKeys';
import type { createPublicApiClient } from './publicApi';

/**
 * Manager-owned keys embedded in the established CPAMP layout.
 *
 * Embedded Fresh New deliberately has no CPA Management Key. Keeping these
 * resources on the canonical Fresh Public API lets the existing MainLayout
 * remain the product shell while avoiding a fake /v0/management connection.
 */
export function FreshManagedKeysPage({
  client,
  credential,
  onUnauthorized,
  onPending,
  visible = true,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  visible?: boolean;
}) {
  return (
    <div className="fresh-managed-config key-managed-page" data-testid="fresh-managed-keys">
      <FreshApiKeys
        visible={visible}
        client={client}
        credential={credential}
        onUnauthorized={onUnauthorized}
        onCreationPending={onPending}
        storedBadge={w('stored')}
        pageDescription={w('keysHelp')}
      />
    </div>
  );
}
