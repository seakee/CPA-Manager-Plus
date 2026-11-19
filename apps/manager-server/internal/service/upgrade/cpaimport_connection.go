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
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type sourceConnectionProvenance struct {
	recorded   model.UpgradeOperation
	sourceRoot string
	cpaDir     string
	cpaAuthDir string
	targetRoot string
	bundleDir  string
	scratchDir string
}

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
func handDestinationConnection(
	ctx context.Context,
	databasePath, dataKeyPath, baseURL, managementKey string,
	provenance sourceConnectionProvenance,
) error {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return err
	}
	destinationURL := cpa.NormalizeBaseURL(baseURL)
	secret := strings.TrimSpace(managementKey)
	if destinationURL == "" || secret == "" {
		return errors.New("the destination connection is incomplete, so the Manager configuration was not changed")
	}
	if err := refuseConflictingOperationBinding(provenance.recorded); err != nil {
		return err
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

	if err := refuseFreshIdentitySettings(ctx, st.SQLDB()); err != nil {
		return err
	}
	if _, bound, err := readExternalBindingFrom(ctx, st.SQLDB()); err != nil {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict, "the recorded External binding could not be inspected", err)
	} else if bound {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict, "the Manager store already records an External target binding", nil)
	}
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
	_, hasV2Revision, err := st.Settings.LoadPersistedCPAConnectionRevision(ctx)
	if err != nil {
		return fmt.Errorf("load persisted CPA connection revision: %w", err)
	}
	input := managerconfig.LegacyConnection{BaseURL: destinationURL, ManagementKey: secret}
	if resolution.Authority != managerconfig.LegacyConnectionAuthorityNone {
		storedURL := cpa.NormalizeBaseURL(resolution.Connection.BaseURL)
		storedKey := strings.TrimSpace(resolution.Connection.ManagementKey)
		sameStagedBinding := storedURL == destinationURL && security.EqualHMAC(storedKey, secret)
		if !sameStagedBinding {
			if hasV2Revision || !matchesUnchangedSourceConnection(ctx, provenance, storedURL, storedKey) {
				return newUpgradeError(UpgradeErrorCPAConnectionConflict, fmt.Sprintf(
					"the Manager already holds a CPA connection to %s, and this import will not repoint it at %s; the destination was staged and its secret stays recorded, so repair the stored connection with `cpa-manager-plus store-cpa-connection --repair-conflict` while Manager Server is stopped and re-run the import",
					redactURLUserinfo(storedURL), redactURLUserinfo(destinationURL)), nil)
			}
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

func refuseConflictingOperationBinding(recorded model.UpgradeOperation) error {
	if recorded.SourceKind != SourceKindUpgradedLegacy ||
		recorded.HasDomain(model.UpgradeDomainIdentity) ||
		ObserveTargetMode(recorded, false) == model.UpgradeTargetExternal {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict,
			"the Manager store already records a conflicting target binding, so this import did not overwrite its CPA connection", nil)
	}
	return nil
}

func refuseFreshIdentitySettings(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx,
		`select count(*) from settings where key in (?, ?, 'fresh_adoption_v1')`,
		freshstore.OperationKey, freshstore.AuthorityKey,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("inspect v2 Fresh identity rows: %w", err)
	}
	if count > 0 {
		return newUpgradeError(UpgradeErrorCPAConnectionConflict,
			"the Manager store carries a v2 Fresh installation identity, so this import did not overwrite its CPA connection", nil)
	}
	return nil
}

// matchesUnchangedSourceConnection proves that the stored legacy connection in
// the target store was copied verbatim from the fenced 1.x source rather than
// authored or rebound on v2. The 1.x database is inspected through OpenSnapshot
// in private scratch space so no WAL/SHM sidecars are ever created in the
// preserved source directory.
func matchesUnchangedSourceConnection(
	ctx context.Context,
	provenance sourceConnectionProvenance,
	storedURL, storedKey string,
) bool {
	sourceRoot := strings.TrimSpace(provenance.sourceRoot)
	if sourceRoot == "" || storedURL == "" || storedKey == "" {
		return false
	}
	sourceRoot = canonicalPath(sourceRoot)
	if wantAfter := strings.TrimSpace(provenance.recorded.SourceFingerprintAfter); wantAfter != "" {
		observed, err := fingerprintSource(ctx, sourceRoot)
		if err != nil || observed.Digest != wantAfter {
			return false
		}
	}
	sourceDatabasePath := filepath.Join(sourceRoot, databaseFileName)
	sourceDataKeyPath := filepath.Join(sourceRoot, dataKeyFileName)
	if _, err := inspectSourceDatabase(sourceDatabasePath); err != nil {
		return false
	}
	sourceKey, err := security.LoadExistingDataKey("", sourceDataKeyPath)
	if err != nil {
		return false
	}
	sourceProtector, err := security.NewProtector(sourceKey)
	if err != nil {
		return false
	}
	preserved := PreservedSource{Database: sourceDatabasePath, DataKey: sourceDataKeyPath}
	cpaDir, cpaAuthDir := ResolveCPAImportRoots(sourceRoot, provenance.cpaDir, provenance.cpaAuthDir)
	extraRoots := []ScratchRoot{
		{Label: "the target root", Dir: strings.TrimSpace(provenance.targetRoot)},
		{Label: "the configured source root", Dir: sourceRoot},
		{Label: "the 1.x CPA directory", Dir: cpaDir},
		{Label: "the 1.x CPA credential directory", Dir: cpaAuthDir},
		{Label: "the backup directory", Dir: strings.TrimSpace(provenance.bundleDir)},
	}
	for _, cfgLegacy := range ConfiguredLegacyRoots() {
		extraRoots = append(extraRoots, ScratchRoot{Label: "the configured legacy source root", Dir: cfgLegacy})
	}
	for _, cfgBundle := range ConfiguredBundleRoots() {
		extraRoots = append(extraRoots, ScratchRoot{Label: "the configured backup directory", Dir: cfgBundle})
	}
	scratchBase := strings.TrimSpace(provenance.scratchDir)
	if err := ValidatePreservedScratchDir(scratchBase, preserved, extraRoots...); err != nil {
		return false
	}
	snapshot, err := OpenSnapshot(ctx, sourceDatabasePath, scratchBase)
	if err != nil {
		return false
	}
	defer func() { _ = snapshot.Close() }()

	sourceSettings := setting.New(snapshot.DB(), sourceProtector)
	srcCfg, srcCfgOK, err := sourceSettings.LoadManagerConfig(ctx)
	if err != nil {
		return false
	}
	srcSetup, srcSetupOK, err := sourceSettings.LoadSetup(ctx)
	if err != nil {
		return false
	}
	srcResolution, err := managerconfig.ResolveLegacyConnectionAuthority(srcCfg, srcCfgOK, srcSetup, srcSetupOK)
	if err != nil || srcResolution.Authority == managerconfig.LegacyConnectionAuthorityNone {
		return false
	}
	return cpa.NormalizeBaseURL(srcResolution.Connection.BaseURL) == storedURL &&
		security.EqualHMAC(strings.TrimSpace(srcResolution.Connection.ManagementKey), storedKey)
}

// redactURLUserinfo strips any embedded user:password@ userinfo from an endpoint
// URL before including it in operator-facing error messages.
func redactURLUserinfo(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.User != nil {
		parsed.User = nil
		return parsed.String()
	}
	if index := strings.Index(trimmed, "://"); index >= 0 {
		rest := trimmed[index+3:]
		end := strings.IndexAny(rest, "/?#")
		authority := rest
		suffix := ""
		if end >= 0 {
			authority = rest[:end]
			suffix = rest[end:]
		}
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			return trimmed[:index+3] + authority[at+1:] + suffix
		}
	}
	return trimmed
}
