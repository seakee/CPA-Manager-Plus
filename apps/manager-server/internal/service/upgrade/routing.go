package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// AccessRoutingOptions configures Upgrade access prefixes on the single
// upgrade_operations row before completion.
type AccessRoutingOptions struct {
	TargetRoot        string
	ControlBasePath   string
	InferenceBasePath string
	SharedEntry       bool
}

// AccessRoutingResult reports the persisted Upgrade routing configuration and
// the derived target mode ("managed", "external", or "" before target selection).
type AccessRoutingResult struct {
	Operation       model.UpgradeOperation
	TargetMode      string
	ExternalBaseURL string
}

// ObserveTargetMode derives the Upgrade target mode from a recorded operation
// and optional External binding without trusting caller claims.
func ObserveTargetMode(op model.UpgradeOperation, hasExternalBinding bool) string {
	if op.Stage == model.UpgradeStageComplete {
		if evidence, err := op.VerificationEvidence(); err == nil && (evidence.TargetMode == model.UpgradeTargetExternal || evidence.TargetMode == model.UpgradeTargetManaged) {
			return evidence.TargetMode
		}
	}
	if hasExternalBinding || op.Stage == model.UpgradeStageExternalConnect || op.HasDomain(model.UpgradeDomainIdentity) {
		return model.UpgradeTargetExternal
	}
	if op.Stage == model.UpgradeStageCPAImport || op.HasDomain(model.UpgradeDomainCPAConfig) {
		return model.UpgradeTargetManaged
	}
	return ""
}

// FencedExternalBaseURL validates and normalizes the External CPA base URL from
// upgrade_external_connect, failing closed whenever the binding is absent,
// lacks a sealed management-key envelope, has a normalized address whose
// canonical identity diverges from binding.Identity, or diverges from recorded
// verification evidence identity/generation/observation time.
func FencedExternalBaseURL(op model.UpgradeOperation, binding upgradeoperation.ExternalBinding, hasBinding bool) string {
	if !hasBinding {
		return ""
	}
	if strings.TrimSpace(binding.Identity) == "" ||
		strings.TrimSpace(binding.Generation) == "" ||
		binding.Identity != binding.Generation ||
		binding.ObservedAtMS <= 0 ||
		!security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
		return ""
	}
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(binding.BaseURL))
	if err != nil {
		return ""
	}
	canonical, err := identity.CanonicalExternalURL(normalized)
	if err != nil || freshkeyapply.ExternalIdentity(canonical) != binding.Identity {
		return ""
	}
	if op.Stage == model.UpgradeStageComplete {
		evidence, err := op.VerificationEvidence()
		if err != nil ||
			!evidence.Complete() ||
			evidence.TargetMode != model.UpgradeTargetExternal ||
			evidence.BindingIdentity != binding.Identity ||
			evidence.BindingGeneration != binding.Generation ||
			evidence.BindingObservedAtMS != binding.ObservedAtMS {
			return ""
		}
		return canonical
	}
	trimmed := bytes.TrimSpace(op.Verification)
	if len(trimmed) > 0 && string(trimmed) != "{}" {
		evidence, err := op.VerificationEvidence()
		if err != nil ||
			evidence.TargetMode != model.UpgradeTargetExternal ||
			evidence.BindingIdentity != binding.Identity ||
			evidence.BindingGeneration != binding.Generation ||
			(evidence.BindingObservedAtMS != 0 && evidence.BindingObservedAtMS != binding.ObservedAtMS) {
			return ""
		}
	}
	return canonical
}

// FencedExternalBaseURLForStore extends FencedExternalBaseURL by verifying the
// sealed credential (when protector is supplied) and checking that any stored
// Manager connection rows (manager_config / setup) still agree with the
// committed External target URL and credential.
func FencedExternalBaseURLForStore(
	ctx context.Context,
	db *sql.DB,
	protector *security.Protector,
	op model.UpgradeOperation,
	binding upgradeoperation.ExternalBinding,
	hasBinding bool,
) string {
	canonical := FencedExternalBaseURL(op, binding, hasBinding)
	if canonical == "" {
		return ""
	}
	var secret string
	if protector != nil {
		unprotected, err := protector.UnprotectString(strings.TrimSpace(binding.ManagementKey))
		if err != nil || !cpa.ValidV8ManagementKey(strings.TrimSpace(unprotected)) {
			return ""
		}
		secret = strings.TrimSpace(unprotected)
	}
	if db == nil {
		return canonical
	}
	if protector != nil {
		var tbl int
		if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'settings'`).Scan(&tbl); err != nil {
			return ""
		}
		if tbl == 0 {
			return canonical
		}
		st := store.New(db, protector)
		managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
		if err != nil {
			return ""
		}
		setupCfg, setupOK, err := st.LoadSetup(ctx)
		if err != nil {
			return ""
		}
		conn, hasAuthority, err := resolveStrictStoredConnection(managerCfg, managerOK, setupCfg, setupOK)
		if err != nil {
			return ""
		}
		checkStrictURL := func(rawURL string) bool {
			trimmed := strings.TrimSpace(rawURL)
			if trimmed == "" {
				return true
			}
			storedCanon, err := canonicalVerifyAddress(trimmed)
			return err == nil && storedCanon == canonical
		}
		if hasAuthority {
			if !checkStrictURL(conn.BaseURL) || !security.EqualHMAC(strings.TrimSpace(conn.ManagementKey), secret) {
				return ""
			}
			return canonical
		}
		if managerOK {
			if !checkStrictURL(managerCfg.CPAConnection.CPABaseURL) {
				return ""
			}
			if k := strings.TrimSpace(managerCfg.CPAConnection.ManagementKey); k != "" && !security.EqualHMAC(k, secret) {
				return ""
			}
		}
		if setupOK {
			if !checkStrictURL(setupCfg.CPAUpstreamURL) {
				return ""
			}
			if k := strings.TrimSpace(setupCfg.ManagementKey); k != "" && !security.EqualHMAC(k, secret) {
				return ""
			}
		}
		return canonical
	}
	if !storedSettingsConnectionAddressesMatch(ctx, db, canonical) {
		return ""
	}
	return canonical
}

// resolveStrictStoredConnection resolves the stored CPA connection from raw
// manager_config_v1 and setup rows without applying legacy 1.x URL stripping
// (/v0, /v0/management) or schemeless http:// insertion.
func resolveStrictStoredConnection(
	managerCfg model.ManagerConfig,
	managerOK bool,
	setup model.Setup,
	setupOK bool,
) (managerconfig.LegacyConnection, bool, error) {
	var mURL, mKey string
	if managerOK {
		mURL = strings.TrimSpace(managerCfg.CPAConnection.CPABaseURL)
		mKey = strings.TrimSpace(managerCfg.CPAConnection.ManagementKey)
	}
	var sURL, sKey string
	if setupOK {
		sURL = strings.TrimSpace(setup.CPAUpstreamURL)
		sKey = strings.TrimSpace(setup.ManagementKey)
	}
	if mURL != "" && mKey != "" {
		return managerconfig.LegacyConnection{BaseURL: mURL, ManagementKey: mKey}, true, nil
	}
	strictEqual := func(u1, u2 string) bool {
		c1, err1 := canonicalVerifyAddress(u1)
		c2, err2 := canonicalVerifyAddress(u2)
		if err1 != nil || err2 != nil {
			return strings.TrimSpace(u1) == strings.TrimSpace(u2)
		}
		return c1 == c2
	}
	if sURL == "" || sKey == "" {
		if mURL != "" && sURL != "" && !strictEqual(mURL, sURL) {
			return managerconfig.LegacyConnection{}, false, errors.New(
				"manager_config_v1 contains a partial CPA connection whose URL conflicts with the legacy setup",
			)
		}
		if mKey != "" && sKey != "" && !security.EqualHMAC(mKey, sKey) {
			return managerconfig.LegacyConnection{}, false, errors.New(
				"manager_config_v1 contains a partial CPA connection whose key conflicts with the legacy setup",
			)
		}
		return managerconfig.LegacyConnection{}, false, nil
	}
	if mURL != "" && !strictEqual(mURL, sURL) {
		return managerconfig.LegacyConnection{}, false, errors.New(
			"legacy setup CPA connection conflicts with the present manager_config_v1 URL",
		)
	}
	if mKey != "" && !security.EqualHMAC(mKey, sKey) {
		return managerconfig.LegacyConnection{}, false, errors.New(
			"legacy setup CPA connection conflicts with the present manager_config_v1 key",
		)
	}
	return managerconfig.LegacyConnection{BaseURL: sURL, ManagementKey: sKey}, true, nil
}

func storedSettingsConnectionAddressesMatch(ctx context.Context, db *sql.DB, canonical string) bool {
	var tbl int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'settings'`).Scan(&tbl); err != nil {
		return false
	}
	if tbl == 0 {
		return true
	}
	checkRawURL := func(rawURL string) bool {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			return true
		}
		norm, err := cpa.NormalizeV8BaseURL(trimmed)
		if err != nil {
			return false
		}
		canon, err := identity.CanonicalExternalURL(norm)
		return err == nil && canon == canonical
	}
	var rawManager string
	var manager model.ManagerConfig
	if err := db.QueryRowContext(ctx, `select value from settings where key = 'manager_config_v1'`).Scan(&rawManager); err == nil {
		if json.Unmarshal([]byte(rawManager), &manager) != nil {
			return false
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var rawSetup string
	var setup model.Setup
	if err := db.QueryRowContext(ctx, `select value from settings where key = 'setup'`).Scan(&rawSetup); err == nil {
		if json.Unmarshal([]byte(rawSetup), &setup) != nil {
			return false
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	// Complete manager_config_v1 is authoritative over a leftover 1.x setup.
	// Without a protector only URL consistency and credential presence can be
	// checked: ciphertext equality is not plaintext credential equality.
	if strings.TrimSpace(manager.CPAConnection.CPABaseURL) != "" && strings.TrimSpace(manager.CPAConnection.ManagementKey) != "" {
		return checkRawURL(manager.CPAConnection.CPABaseURL)
	}
	return checkRawURL(manager.CPAConnection.CPABaseURL) && checkRawURL(setup.CPAUpstreamURL)
}

// ConfigureAccessRouting persists operator-confirmed Upgrade prefixes onto the
// single upgrade_operations row behind its revision CAS and process lock.
func ConfigureAccessRouting(ctx context.Context, options AccessRoutingOptions) (AccessRoutingResult, error) {
	if err := ctx.Err(); err != nil {
		return AccessRoutingResult{}, err
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root is not usable, so there is no recorded operation to configure", err)
	}
	paths := newTargetPaths(targetRoot)
	targetLock, err := processlock.Acquire(paths.Database)
	if err != nil {
		if errors.Is(err, processlock.ErrLocked) {
			return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyV2Running,
				"another process holds the v2 data-root fence, so access prefixes cannot be written right now", err)
		}
		return AccessRoutingResult{}, fmt.Errorf("acquire the v2 data-root fence: %w", err)
	}
	defer func() { _ = targetLock.Close() }()

	if err := requireRecoveredCopyIn(paths.Database); err != nil {
		return AccessRoutingResult{}, err
	}
	if _, err := os.Stat(paths.Database); err != nil {
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root holds no recorded copy-in yet; run copy-in before configuring access prefixes", err)
	}

	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		return AccessRoutingResult{}, fmt.Errorf("open the Manager database: %w", err)
	}
	defer func() { _ = db.Close() }()

	repo := upgradeoperation.New(db)
	current, found, err := repo.Get(ctx)
	if err != nil {
		return AccessRoutingResult{}, verifyRecordReadError(err)
	}
	if !found {
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"no recorded upgrade operation exists yet", nil)
	}
	binding, hasBinding, err := repo.ExternalBinding(ctx)
	if err != nil {
		return AccessRoutingResult{}, fmt.Errorf("read the External binding: %w", err)
	}
	if current.Stage == model.UpgradeStageRolledBack {
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the upgrade operation was rolled back and cannot be modified", nil)
	}
	if current.Stage == model.UpgradeStageComplete {
		verified, err := completedVerifyResultForStore(ctx, db, paths, current, binding, hasBinding)
		if err != nil {
			return AccessRoutingResult{}, err
		}
		return repairOrReplayCompletedRouting(ctx, repo, current, verified.TargetMode, verified.ExternalBaseURL, options.ControlBasePath, options.InferenceBasePath, options.SharedEntry)
	}
	mode := ObserveTargetMode(current, hasBinding)
	extURL := ""
	if mode == model.UpgradeTargetExternal {
		extURL = FencedExternalBaseURLForStore(ctx, db, nil, current, binding, hasBinding)
	}
	if current.RoutingCommitted {
		if current.ControlBasePath == options.ControlBasePath &&
			current.InferenceBasePath == options.InferenceBasePath &&
			current.SharedEntry == options.SharedEntry &&
			(options.InferenceBasePath == "" || current.InferenceCommitted) {
			return AccessRoutingResult{Operation: current, TargetMode: mode, ExternalBaseURL: extURL}, nil
		}
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
			"the access prefixes are already committed and preserved across upgrades", nil)
	}

	external := mode == model.UpgradeTargetExternal
	if mode == "" {
		external = options.InferenceBasePath == "" && !options.SharedEntry
	}
	if err := routing.ValidateAccessPrefixPair(options.ControlBasePath, options.InferenceBasePath, options.SharedEntry, external); err != nil {
		return AccessRoutingResult{}, mapAccessPrefixToUpgradeError(err)
	}
	if err := ensureSuggestedPrefixes(&current); err != nil {
		return AccessRoutingResult{}, err
	}
	updated, err := repo.RecordRouting(ctx, options.ControlBasePath, options.InferenceBasePath, options.SharedEntry, current.SuggestedControlBasePath, current.SuggestedInferenceBasePath)
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrRoutingAlreadyCommitted) {
			return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
				"the access prefixes are already committed and preserved across upgrades", err)
		}
		return AccessRoutingResult{}, mapVerifyRecordError(err)
	}
	if mode == model.UpgradeTargetExternal {
		extURL = FencedExternalBaseURLForStore(ctx, db, nil, updated, binding, hasBinding)
	}
	return AccessRoutingResult{Operation: updated, TargetMode: mode, ExternalBaseURL: extURL}, nil
}

func completedOperationFullyRouted(op model.UpgradeOperation, mode string) (bool, error) {
	switch mode {
	case model.UpgradeTargetExternal:
		if !op.RoutingCommitted && strings.TrimSpace(op.ControlBasePath) == "" &&
			strings.TrimSpace(op.InferenceBasePath) == "" && !op.SharedEntry && !op.InferenceCommitted {
			return false, nil
		}
		if !op.RoutingCommitted || strings.TrimSpace(op.ControlBasePath) == "" ||
			op.InferenceBasePath != "" || op.SharedEntry || op.InferenceCommitted {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation carries inconsistent External routing columns", nil)
		}
		if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, "", false, true); err != nil {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation carries an invalid committed control prefix", err)
		}
		return true, nil
	case model.UpgradeTargetManaged:
		if !op.RoutingCommitted && strings.TrimSpace(op.ControlBasePath) == "" &&
			!op.InferenceCommitted && strings.TrimSpace(op.InferenceBasePath) == "" && !op.SharedEntry {
			return false, nil
		}
		if !op.RoutingCommitted || strings.TrimSpace(op.ControlBasePath) == "" {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation carries inconsistent Managed routing columns", nil)
		}
		if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, "", false, true); err != nil {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation carries an invalid committed control prefix", err)
		}
		if !op.InferenceCommitted {
			if op.InferenceBasePath != "" {
				if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false); err != nil {
					return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
						"the recorded completed operation carries an invalid staged inference prefix", err)
				}
			} else if op.SharedEntry {
				return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
					"the recorded completed Managed operation carries sharedEntry without an inference prefix", nil)
			}
			return false, nil
		}
		if strings.TrimSpace(op.InferenceBasePath) == "" {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation claims committed inference without a prefix", nil)
		}
		if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false); err != nil {
			return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded completed operation carries invalid committed routing prefixes", err)
		}
		return true, nil
	default:
		return false, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded completed operation carries an unsupported target mode", nil)
	}
}

func repairOrReplayCompletedRouting(
	ctx context.Context,
	repo *upgradeoperation.Repository,
	current model.UpgradeOperation,
	mode string,
	extURL string,
	controlBasePath string,
	inferenceBasePath string,
	sharedEntry bool,
) (AccessRoutingResult, error) {
	fullyRouted, err := completedOperationFullyRouted(current, mode)
	if err != nil {
		return AccessRoutingResult{}, err
	}
	if fullyRouted {
		if controlBasePath == current.ControlBasePath &&
			inferenceBasePath == current.InferenceBasePath &&
			sharedEntry == current.SharedEntry {
			return AccessRoutingResult{Operation: current, TargetMode: mode, ExternalBaseURL: extURL}, nil
		}
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
			"the access prefixes are already committed and preserved across upgrades", nil)
	}
	if controlBasePath == "" && inferenceBasePath == "" && !sharedEntry {
		return AccessRoutingResult{}, newUpgradeError(UpgradeErrorVerifyAccessPrefixRequired,
			"the access prefixes for this target mode have not been configured yet", nil)
	}
	ctrl := controlBasePath
	if current.RoutingCommitted && strings.TrimSpace(current.ControlBasePath) != "" {
		if ctrl != "" && ctrl != current.ControlBasePath {
			return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
				"the control prefix is already committed and cannot be modified", nil)
		}
		ctrl = current.ControlBasePath
	}
	if !current.InferenceCommitted && current.InferenceBasePath != "" && (inferenceBasePath != "" || sharedEntry) {
		if inferenceBasePath != current.InferenceBasePath || sharedEntry != current.SharedEntry {
			return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
				"a different staged inference prefix is already recorded on the completed upgrade", nil)
		}
	}
	isExternal := mode == model.UpgradeTargetExternal
	if err := routing.ValidateAccessPrefixPair(ctrl, inferenceBasePath, sharedEntry, isExternal); err != nil {
		return AccessRoutingResult{}, mapAccessPrefixToUpgradeError(err)
	}
	if err := ensureSuggestedPrefixes(&current); err != nil {
		return AccessRoutingResult{}, err
	}
	updated, err := repo.RepairMissingRoutingOnCompletedUpgrade(ctx, current.Revision, ctrl, inferenceBasePath, sharedEntry, current.SuggestedControlBasePath, current.SuggestedInferenceBasePath)
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrRoutingAlreadyCommitted) {
			return AccessRoutingResult{}, newUpgradeError(UpgradeErrorPrefixesAlreadyCommitted,
				"the access prefixes are already committed and preserved across upgrades", err)
		}
		return AccessRoutingResult{}, mapVerifyRecordError(err)
	}
	return AccessRoutingResult{Operation: updated, TargetMode: mode, ExternalBaseURL: extURL}, nil
}

func mapAccessPrefixToUpgradeError(err error) error {
	if code, ok := bootstrap.AccessPrefixCodeOf(err); ok {
		return newUpgradeError(UpgradeErrorCode(code), "the requested access prefix configuration is not valid", err)
	}
	return newUpgradeError(UpgradeErrorCode(bootstrap.CodeInvalidAccessPrefix), "the requested access prefix configuration is not valid", err)
}

// Suggestions are created only on a writable upgrade path and persisted with
// the operation, just like Fresh. Reads never derive or apply default routes.
func ensureSuggestedPrefixes(op *model.UpgradeOperation) error {
	if op.SuggestedControlBasePath != "" && op.SuggestedInferenceBasePath != "" {
		return nil
	}
	control, inference, err := bootstrap.GenerateSuggestedPrefixPair(func(candidate string) bool {
		if routing.ReservedNamespaceCollision(candidate) {
			return true
		}
		for _, existing := range []string{op.ControlBasePath, op.SuggestedControlBasePath, op.SuggestedInferenceBasePath} {
			if existing != "" && bootstrap.SameOrNestedBasePath(existing, candidate) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("generate upgrade access prefix suggestions: %w", err)
	}
	if op.SuggestedControlBasePath == "" {
		op.SuggestedControlBasePath = control
	}
	if op.SuggestedInferenceBasePath == "" {
		op.SuggestedInferenceBasePath = inference
	}
	return nil
}
