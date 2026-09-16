package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Result struct {
	GeneratedAdminKey string
	AdminCreated      bool
	DataKeyCreated    bool
	MigratedLegacy    bool
	HasHistoricalData bool
	State             store.BootstrapState
}

// currentConnectionStorageMigrationVersion is the version of the
// manager_config/setup normalization migration. Version 1 is the legacy
// MigratedLegacy boolean; version 2 adds authoritative reconciliation,
// partial-manager repair, and encrypted rewrites. Databases migrated by older
// releases carry no version field and decode as 0, so the migration runs once
// more under this release.
const currentConnectionStorageMigrationVersion = 2

func Run(ctx context.Context, cfg config.Config, st *store.Store, dataKeyCreated bool) (Result, error) {
	result := Result{DataKeyCreated: dataKeyCreated}

	historical, err := st.HasHistoricalData(ctx)
	if err != nil {
		return Result{}, err
	}
	result.HasHistoricalData = historical

	previousState, stateFound, err := st.LoadBootstrapState(ctx)
	if err != nil {
		return Result{}, err
	}
	connectionStorageMigrationVersion := 0
	if stateFound {
		connectionStorageMigrationVersion = previousState.ConnectionStorageMigrationVersion
	}
	// The version gate, not MigratedLegacy, decides whether the connection
	// normalization runs: older releases already set MigratedLegacy=true
	// without performing it. The version is only persisted after the
	// migration succeeds, so a failed normalization retries on the next boot.
	needsConnectionStorageMigration := !stateFound ||
		!previousState.MigratedLegacy ||
		previousState.ConnectionStorageMigrationVersion < currentConnectionStorageMigrationVersion
	if needsConnectionStorageMigration {
		migrated, err := migrateLegacyConfig(ctx, cfg, st)
		if err != nil {
			return Result{}, err
		}
		if migrated || (stateFound && previousState.MigratedLegacy) {
			result.MigratedLegacy = true
		}
		connectionStorageMigrationVersion = currentConnectionStorageMigrationVersion
	} else {
		result.MigratedLegacy = previousState.MigratedLegacy
	}
	if _, err := initializeEmbeddedRuntimeDesiredState(ctx, cfg, st); err != nil {
		return Result{}, fmt.Errorf("initialize embedded Runtime desired state: %w", err)
	}

	projectInitialized, err := projectInitialized(ctx, cfg, st)
	if err != nil {
		return Result{}, err
	}
	state := store.BootstrapState{
		Version:                           1,
		Status:                            bootstrapStatus(projectInitialized, historical),
		AdminReady:                        true,
		ProjectInitialized:                projectInitialized,
		DataKeyReady:                      true,
		MigratedLegacy:                    result.MigratedLegacy,
		HasHistoricalData:                 historical,
		ConnectionStorageMigrationVersion: connectionStorageMigrationVersion,
	}
	if err := st.SaveBootstrapState(ctx, state); err != nil {
		return Result{}, err
	}
	state, _, _ = st.LoadBootstrapState(ctx)
	result.State = state

	// Admin credential persistence is deliberately the last fallible write of
	// bootstrap: a randomly generated key must never be persisted unless every
	// earlier step succeeded, so its plaintext can always be disclosed by the
	// caller instead of locking the operator out with an unknown credential.
	// A crash between the state write and this write self-heals on the next
	// boot, which simply generates and discloses a fresh key.
	adminCreated, generatedAdminKey, err := ensureAdminCredential(ctx, cfg, st)
	if err != nil {
		return Result{}, err
	}
	result.AdminCreated = adminCreated
	result.GeneratedAdminKey = generatedAdminKey
	return result, nil
}

func initializeEmbeddedRuntimeDesiredState(
	ctx context.Context,
	cfg config.Config,
	st *store.Store,
) (bool, error) {
	if !cfg.EmbeddedRuntimeConfigured() {
		return false, nil
	}
	if _, found, err := st.LoadEmbeddedRuntimeDesiredState(ctx); err != nil || found {
		return false, err
	}
	// A complete environment-provided CPA connection is existing external
	// authority even when the legacy settings rows are absent.
	if strings.TrimSpace(cfg.CPAUpstreamURL) != "" && strings.TrimSpace(cfg.ManagementKey) != "" {
		return false, nil
	}
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return false, err
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return false, err
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	if err != nil {
		// Ambiguous historical authority is never a reason to adopt Embedded
		// lifecycle ownership.
		return false, nil
	}
	if resolution.Authority != managerconfig.LegacyConnectionAuthorityNone {
		return false, nil
	}
	if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(ctx, model.EmbeddedRuntimeDesiredRunning); err != nil {
		return false, err
	}
	return true, nil
}

func ensureAdminCredential(ctx context.Context, cfg config.Config, st *store.Store) (bool, string, error) {
	if _, ok, err := st.LoadAdminCredential(ctx); err != nil || ok {
		return false, "", err
	}
	adminKey := cfg.AdminKey
	source := "env"
	if adminKey == "" {
		generated, err := security.GenerateAdminKey()
		if err != nil {
			return false, "", err
		}
		adminKey = generated
		source = "generated"
	}
	credential, err := security.NewAdminCredential(adminKey, source)
	if err != nil {
		return false, "", err
	}
	if err := st.SaveAdminCredential(ctx, credential); err != nil {
		return false, "", err
	}
	if source == "generated" {
		return true, adminKey, nil
	}
	return true, "", nil
}

func migrateLegacyConfig(ctx context.Context, cfg config.Config, st *store.Store) (bool, error) {
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return false, err
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return false, err
	}

	resolution, resolveErr := managerconfig.ResolveLegacyConnectionAuthority(
		managerCfg,
		managerOK,
		setup,
		setupOK,
	)

	// Secret-at-rest normalization is deliberately independent from authority
	// resolution. Even a conflicting or otherwise unusable historical pair is
	// rewritten through the migration-only transactional path before the
	// authority error is returned. This prevents a failed migration from
	// preserving plaintext keys while still refusing to guess a connection.
	if resolveErr != nil {
		if err := st.NormalizeLegacyConnectionStorage(ctx, managerCfg, managerOK, setup, setupOK); err != nil {
			return false, fmt.Errorf("normalize legacy CPA connection storage: %w", err)
		}
		return false, fmt.Errorf("%w%s", resolveErr, managerconfig.LegacyConnectionConflictRepairHint)
	}

	var normalizedManager store.ManagerConfig
	managerPresent := managerOK
	var normalizedSetup store.Setup
	setupPresent := setupOK
	switch resolution.Authority {
	case managerconfig.LegacyConnectionAuthorityManager:
		// The complete manager row is authoritative. A stale or partial setup
		// is canonicalized from it, while its collector fields are retained.
		normalizedManager = managerCfg
		managerconfig.MergeLegacyCollectorSettings(&normalizedManager, setup, setupOK)
		normalizedSetup = managerconfig.CanonicalSetupFromManagerConfig(normalizedManager, setup, setupOK)
		setupPresent = true
	case managerconfig.LegacyConnectionAuthoritySetup:
		// A complete setup can repair a partial manager only after the shared
		// resolver has confirmed every existing manager side matches it.
		if !managerOK {
			normalizedManager = managerConfigFromSetup(cfg, setup)
		} else {
			normalizedManager = managerCfg
			normalizedManager.CPAConnection.CPABaseURL = resolution.Connection.BaseURL
			normalizedManager.CPAConnection.ManagementKey = resolution.Connection.ManagementKey
			managerconfig.MergeLegacyCollectorSettings(&normalizedManager, setup, setupOK)
		}
		managerPresent = true
		normalizedSetup = managerconfig.CanonicalSetupFromManagerConfig(normalizedManager, setup, setupOK)
		setupPresent = true
	default:
		// No complete authority exists. Preserve each partial historical row
		// independently; in particular, never combine a manager URL with a
		// setup-only key during bootstrap.
		normalizedManager = managerCfg
		normalizedSetup = setup
	}

	if err := st.NormalizeLegacyConnectionStorage(
		ctx,
		normalizedManager,
		managerPresent,
		normalizedSetup,
		setupPresent,
	); err != nil {
		return false, fmt.Errorf("normalize legacy CPA connection storage: %w", err)
	}
	return managerPresent || setupPresent, nil
}

func managerConfigFromSetup(cfg config.Config, setup store.Setup) store.ManagerConfig {
	pollIntervalMS := int(cfg.PollInterval / time.Millisecond)
	return store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    cpa.NormalizeBaseURL(setup.CPAUpstreamURL),
			ManagementKey: setup.ManagementKey,
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:        managerconfig.BoolPtr(true),
			CollectorMode:  managerconfig.CollectorMode(cfg.CollectorMode),
			Queue:          managerconfig.ValueOr(setup.Queue, cfg.Queue),
			PopSide:        managerconfig.NormalizePopSide(setup.PopSide, cfg.PopSide),
			BatchSize:      managerconfig.PositiveOrDefault(cfg.BatchSize, 100, 100),
			PollIntervalMS: managerconfig.PositiveOrDefault(pollIntervalMS, 500, 500),
			QueryLimit:     managerconfig.PositiveOrDefault(cfg.QueryLimit, 50000, 50000),
			TLSSkipVerify:  cfg.TLSSkipVerify,
		},
	}
}

func projectInitialized(ctx context.Context, cfg config.Config, st *store.Store) (bool, error) {
	if cfg.CPAUpstreamURL != "" && cfg.ManagementKey != "" {
		return true, nil
	}
	if managerCfg, ok, err := st.LoadManagerConfig(ctx); err != nil {
		return false, err
	} else if ok && managerCfg.CPAConnection.CPABaseURL != "" && managerCfg.CPAConnection.ManagementKey != "" {
		return true, nil
	}
	if setup, ok, err := st.LoadSetup(ctx); err != nil {
		return false, err
	} else if ok && setup.CPAUpstreamURL != "" && setup.ManagementKey != "" {
		return true, nil
	}
	return false, nil
}

func bootstrapStatus(projectInitialized bool, historical bool) string {
	if projectInitialized {
		if historical {
			return "migrated"
		}
		return "ready"
	}
	if historical {
		return "needs_setup"
	}
	return "fresh"
}
