package upgrade

// This file hands the destination CPA this step proved to the Manager's own
// connection, so the Console can reach it after a restart.
//
// # Why it is a handoff and not a promotion
//
// A connection is not an installation-derived authority: it stores the address
// and the management secret a running Manager uses. Writing it here repairs what
// the import would otherwise destroy — the step staged the destination with a
// secret only this process ever saw, so without the handoff the Console could
// never manage the destination again. Nothing in this file claims a Fresh
// installation, a strategy checkpoint or a qualification; the existing consumers
// still refuse an Upgrade-derived source on their own terms.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// handDestinationConnection stores the proven destination in the Manager's own
// CPA connection before the operation row records the import.
//
// The order is the crash contract. The connection is written first, in its own
// transaction, while the destination attempt still holds the same secret; the
// operation row and the attempt row are written afterwards. A crash between the
// two leaves both copies, and a retry reads the recorded attempt, re-proves the
// destination and finishes. Recording the import first and handing the connection
// over afterwards would make a crash leave neither.
//
// Unrelated Manager settings are preserved: the stored configuration is loaded,
// only its CPA connection is replaced, and the collector, codex inspection and
// external usage settings are written back exactly as they were. A stored
// connection that names a different CPA is refused instead of repointed, and two
// partial rows are never combined into one inferred connection.
func handDestinationConnection(ctx context.Context, databasePath, dataKeyPath, baseURL, managementKey string) error {
	destinationURL := cpa.NormalizeBaseURL(baseURL)
	secret := strings.TrimSpace(managementKey)
	if destinationURL == "" || secret == "" {
		return errors.New("the destination connection is incomplete, so the Manager configuration was not changed")
	}
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		return fmt.Errorf("load the Manager data key: %w", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return fmt.Errorf("initialize the secret protector: %w", err)
	}
	st, err := store.Open(databasePath, protector)
	if err != nil {
		return fmt.Errorf("open the v2 database to store the destination connection: %w", err)
	}
	defer func() { _ = st.Close() }()

	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("load manager_config_v1: %w", err)
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return fmt.Errorf("load the legacy setup row: %w", err)
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict,
			"the stored manager_config_v1 and legacy setup rows disagree about the CPA connection, so this import did not choose a side; repair the stored connection with `cpa-manager-plus store-cpa-connection --repair-conflict` while Manager Server is stopped, then re-run the import", err)
	}
	input := managerconfig.LegacyConnection{BaseURL: destinationURL, ManagementKey: secret}
	if resolution.Authority != managerconfig.LegacyConnectionAuthorityNone {
		// A complete stored connection is the Console's connection. At this
		// destination the same - possibly stale - secret is refreshed; at another
		// CPA it is a CPA the operator still manages, and repointing it silently
		// would take that CPA out of reach.
		if cpa.NormalizeBaseURL(resolution.Connection.BaseURL) != destinationURL {
			return newUpgradeError(UpgradeErrorCPAConnectionConflict, fmt.Sprintf(
				"the Manager already holds a CPA connection to %s, and this import will not repoint it at %s; the destination was staged and its secret stays recorded, so repair the stored connection with `cpa-manager-plus store-cpa-connection --repair-conflict` while Manager Server is stopped and re-run the import",
				cpa.NormalizeBaseURL(resolution.Connection.BaseURL), destinationURL), nil)
		}
	} else if err := resolution.ValidateRequestedLegacyConnection(input); err != nil {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict,
			"a partial stored CPA connection contradicts this destination, and this import does not combine two partial rows into one; repair the stored connection with `cpa-manager-plus store-cpa-connection --repair-conflict` while Manager Server is stopped, then re-run the import", err)
	}

	next := managerCfg
	next.CPAConnection.CPABaseURL = destinationURL
	next.CPAConnection.ManagementKey = secret
	nextSetup := managerconfig.CanonicalSetupFromManagerConfig(next, setup, setupOK)
	if err := st.SaveManagerConfigAndSetup(ctx, next, nextSetup); err != nil {
		return fmt.Errorf("store the destination connection: %w", err)
	}
	// Read it back through the same path the running Manager uses: the handoff is
	// only proven by the value a restart would resolve, not by the write.
	verified, found, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("verify the stored destination connection: %w", err)
	}
	if !found || cpa.NormalizeBaseURL(verified.CPAConnection.CPABaseURL) != destinationURL ||
		!security.EqualHMAC(strings.TrimSpace(verified.CPAConnection.ManagementKey), secret) {
		return errors.New("verify the stored destination connection: the stored value does not match the destination this run proved")
	}
	return nil
}
