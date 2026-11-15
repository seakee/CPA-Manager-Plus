package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// upgradeCollectorController coordinates CollectorWorker startup, active
// collector.Manager runs, and durable External credential refusal state so a
// refused External management credential is persisted once, stops only the
// matching active run, and prevents re-presenting the refused credential
// across Collector polls and process restarts until rebound.
type upgradeCollectorController struct {
	manager *collector.Manager
	cfg     upgradeUsageStartupConfig

	mu          sync.Mutex
	boundRunID  uint64
	boundTarget verifiedUpgradeUsageTarget
}

func newUpgradeCollectorController(
	manager *collector.Manager,
	cfg upgradeUsageStartupConfig,
) *upgradeCollectorController {
	return &upgradeCollectorController{
		manager: manager,
		cfg:     cfg,
	}
}

func (c *upgradeCollectorController) attach() {
	if c == nil || c.manager == nil {
		return
	}
	if c.cfg.CollectorWorker != nil {
		c.cfg.CollectorWorker.SetRuntimeGate(c.allowCollectorWorkerStart)
	}
	if c.cfg.Rejection != nil {
		c.cfg.Rejection.setOnRecord(c.handleExternalRejectionRecorded)
		c.cfg.Rejection.setOnStart(c.noteExternalCollectorStart)
	}
	c.manager.SetAuthRejectionRunHandler(c.handleCollectorAuthRejection)
	c.manager.SetRunningRunHandler(func(ctx context.Context, runID uint64, runtimeCfg collector.RuntimeConfig) bool {
		proven, err := proveUpgradeCollectionStartWithController(ctx, c, c.manager, runID, c.cfg, runtimeCfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Upgrade usage collection start proof error: %v", err)
		}
		return proven
	})
}

func (c *upgradeCollectorController) bindActiveRun(runID uint64, target verifiedUpgradeUsageTarget) {
	if c == nil || target.mode != model.UpgradeTargetExternal || target.targetIdentity == "" || target.credDigest == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if runID != 0 && c.manager != nil && c.manager.StartCount() != runID {
		return
	}
	c.boundRunID = runID
	c.boundTarget = target
}

func (c *upgradeCollectorController) noteExternalCollectorStart(identity, credDigest, baseURL, managementKey string) {
	if c == nil || c.manager == nil || identity == "" || credDigest == "" {
		return
	}
	canonicalURL, _ := canonicalUpgradeUsageURL(baseURL)
	c.mu.Lock()
	c.boundRunID = c.manager.StartCount() + 1
	c.boundTarget = verifiedUpgradeUsageTarget{
		mode:            model.UpgradeTargetExternal,
		committedURL:    baseURL,
		canonicalURL:    canonicalURL,
		committedSecret: strings.TrimSpace(managementKey),
		targetIdentity:  identity,
		credDigest:      credDigest,
	}
	c.mu.Unlock()
}

func (c *upgradeCollectorController) allowCollectorWorkerStart(
	ctx context.Context,
	runtimeCfg collector.RuntimeConfig,
) bool {
	if c == nil || c.cfg.DB == nil || c.cfg.Store == nil || c.cfg.Protector == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	isUpgrade, err := isUpgradeStoreDB(ctx, c.cfg.DB)
	if err != nil {
		return false
	}
	if !isUpgrade {
		return true
	}
	target, ok, err := verifyUpgradeStartupCollectorTarget(ctx, c.cfg, runtimeCfg)
	if err != nil || !ok {
		return false
	}
	if target.mode == model.UpgradeTargetExternal && c.manager != nil {
		c.mu.Lock()
		c.boundRunID = c.manager.StartCount() + 1
		c.boundTarget = target
		c.mu.Unlock()
	}
	return true
}

func isUpgradeStoreDB(ctx context.Context, db *sql.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	repo := upgradeoperation.New(db)
	_, foundOp, err := repo.Get(ctx)
	if err != nil {
		return true, err
	}
	if foundOp {
		return true, nil
	}
	_, hasBinding, err := repo.ExternalBinding(ctx)
	if err != nil {
		return true, err
	}
	if hasBinding {
		return true, nil
	}
	if _, found, err := readUpgradeSettingRaw(ctx, db, upgradeImportDestinationSettingKey); err != nil || found {
		return true, err
	}
	if _, found, err := readUpgradeSettingRaw(ctx, db, upgradeDestinationSettingKey); err != nil || found {
		return true, err
	}
	return false, nil
}

// verifyUpgradeStartupCollectorTarget verifies that an Upgrade store has a
// completed, verified Upgrade operation, aligned connection settings, and an
// unrefused External credential (or verified Managed destination record)
// before CollectorWorker starts collector.Manager. On Managed stores, live
// Supervisor readiness is verified on poll by proveUpgradeCollectionStart so
// a Supervisor still in "starting" at boot can converge once ready.
func verifyUpgradeStartupCollectorTarget(
	ctx context.Context,
	cfg upgradeUsageStartupConfig,
	runtimeCfg collector.RuntimeConfig,
) (verifiedUpgradeUsageTarget, bool, error) {
	if err := ctx.Err(); err != nil {
		return verifiedUpgradeUsageTarget{}, false, err
	}
	upgradeRepo := upgradeoperation.New(cfg.DB)
	op, ev, ok, err := loadVerifiedCompletedUpgradeOperation(ctx, upgradeRepo)
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrInvalidRow) || errors.Is(err, upgradeoperation.ErrVerificationEvidenceMissing) {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
		return verifiedUpgradeUsageTarget{}, false, err
	}
	if !ok {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	if validateCompletedUpgradeRoutingRow(op, ev.TargetMode) != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	if _, _, err := readUpgradeSettingRaw(ctx, cfg.DB, "manager_config_v1"); err != nil {
		return verifiedUpgradeUsageTarget{}, false, err
	}
	if _, _, err := readUpgradeSettingRaw(ctx, cfg.DB, "setup"); err != nil {
		return verifiedUpgradeUsageTarget{}, false, err
	}

	dbCfg, dbCfgOK, err := cfg.Store.LoadManagerConfig(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return verifiedUpgradeUsageTarget{}, false, ctxErr
		}
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	if dbCfgOK && !collectorservice.ManagerCollectorEnabled(dbCfg) {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	setupCfg, setupOK, err := cfg.Store.LoadSetup(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return verifiedUpgradeUsageTarget{}, false, ctxErr
		}
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	persistedResolution, err := managerconfig.ResolveLegacyConnectionAuthority(dbCfg, dbCfgOK, setupCfg, setupOK)
	if err != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}

	activeCanonical, err := canonicalUpgradeUsageURL(runtimeCfg.CPAUpstreamURL)
	if err != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	activeSecret := strings.TrimSpace(runtimeCfg.ManagementKey)
	if !cpa.ValidV8ManagementKey(activeSecret) {
		return verifiedUpgradeUsageTarget{}, false, nil
	}

	if cfg.ManagerConfigService != nil {
		resolvedCfg, _, _, err := cfg.ManagerConfigService.ResolveManagerConfigWithSource(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return verifiedUpgradeUsageTarget{}, false, ctxErr
			}
			return verifiedUpgradeUsageTarget{}, false, nil
		}
		if !collectorservice.ManagerCollectorEnabled(resolvedCfg) {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
		activeQueue := strings.TrimSpace(runtimeCfg.Queue)
		resolvedQueue := strings.TrimSpace(resolvedCfg.Collector.Queue)
		if resolvedQueue != "" && activeQueue != "" && activeQueue != resolvedQueue {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
		resolvedCanonical, err := canonicalUpgradeUsageURL(resolvedCfg.CPAConnection.CPABaseURL)
		if err != nil || resolvedCanonical != activeCanonical {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
		resolvedSecret := strings.TrimSpace(resolvedCfg.CPAConnection.ManagementKey)
		if !cpa.ValidV8ManagementKey(resolvedSecret) || !security.EqualHMAC(resolvedSecret, activeSecret) {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
	}

	var target verifiedUpgradeUsageTarget
	target.mode = ev.TargetMode
	switch ev.TargetMode {
	case model.UpgradeTargetManaged:
		rec, secret, mErr := loadVerifiedUpgradeManagedDestination(ctx, cfg.DB, cfg.Protector)
		if mErr != nil {
			if isUpgradeTargetUnprovenError(mErr) {
				return verifiedUpgradeUsageTarget{}, false, nil
			}
			return verifiedUpgradeUsageTarget{}, false, mErr
		}
		target.committedURL = rec.BaseURL
		target.canonicalURL = rec.CanonicalURL
		target.committedSecret = secret
		target.targetIdentity = string(rec.Intent.ExpectedRuntimeIdentity)
	case model.UpgradeTargetExternal:
		committedURL, canonicalURL, secret, bindingIdentity, credDigest, eErr := verifyUpgradeExternalTargetForUsageStart(ctx, cfg, upgradeRepo, op, ev)
		if eErr != nil {
			if isUpgradeTargetUnprovenError(eErr) {
				return verifiedUpgradeUsageTarget{}, false, nil
			}
			return verifiedUpgradeUsageTarget{}, false, eErr
		}
		target.committedURL = committedURL
		target.canonicalURL = canonicalURL
		target.committedSecret = secret
		target.targetIdentity = bindingIdentity
		target.credDigest = credDigest
	default:
		return verifiedUpgradeUsageTarget{}, false, nil
	}

	if activeCanonical != target.canonicalURL || !security.EqualHMAC(activeSecret, target.committedSecret) {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	if persistedResolution.Authority != managerconfig.LegacyConnectionAuthorityNone {
		persistedCanonical, err := canonicalUpgradeUsageURL(persistedResolution.Connection.BaseURL)
		if err != nil || persistedCanonical != target.canonicalURL ||
			!security.EqualHMAC(strings.TrimSpace(persistedResolution.Connection.ManagementKey), target.committedSecret) {
			return verifiedUpgradeUsageTarget{}, false, nil
		}
	} else if err := persistedResolution.ValidateRequestedLegacyConnection(managerconfig.LegacyConnection{
		BaseURL:       target.committedURL,
		ManagementKey: target.committedSecret,
	}); err != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	return target, true, nil
}

func (c *upgradeCollectorController) handleCollectorAuthRejection(runID uint64) {
	if c == nil || c.manager == nil || c.cfg.Rejection == nil {
		return
	}
	if runID != 0 && c.manager.StartCount() != runID {
		return
	}
	st := c.manager.Status()
	if st.Collector != "stopped" {
		return
	}

	var identity, credDigest string
	c.mu.Lock()
	if (runID == 0 || c.boundRunID == runID) &&
		c.boundTarget.mode == model.UpgradeTargetExternal &&
		c.boundTarget.targetIdentity != "" &&
		c.boundTarget.credDigest != "" {
		identity = c.boundTarget.targetIdentity
		credDigest = c.boundTarget.credDigest
		c.boundRunID = 0
		c.boundTarget = verifiedUpgradeUsageTarget{}
	}
	c.mu.Unlock()

	if identity == "" || credDigest == "" {
		if c.cfg.DB == nil || c.cfg.Protector == nil {
			return
		}
		upgradeRepo := upgradeoperation.New(c.cfg.DB)
		op, ev, ok, err := loadVerifiedCompletedUpgradeOperation(context.Background(), upgradeRepo)
		if err != nil || !ok || ev.TargetMode != model.UpgradeTargetExternal {
			return
		}
		binding, hasBinding, err := upgradeRepo.ExternalBinding(context.Background())
		if err != nil || !hasBinding {
			return
		}
		fencedURL := upgradeservice.FencedExternalBaseURLForStore(context.Background(), c.cfg.DB, c.cfg.Protector, op, binding, hasBinding)
		if fencedURL == "" {
			return
		}
		install, canonicalURL, resolvedDigest, err := loadVerifiedUpgradeExternalBinding(context.Background(), upgradeRepo, c.cfg.Protector, ev)
		if err != nil || canonicalURL != fencedURL {
			return
		}
		upCanon, err := canonicalUpgradeUsageURL(st.Upstream)
		if err != nil || upCanon != canonicalURL {
			return
		}
		identity = install.Identity
		credDigest = resolvedDigest
	}

	if identity == "" || credDigest == "" {
		return
	}
	if runID != 0 && c.manager.StartCount() != runID {
		return
	}
	if err := c.cfg.Rejection.record(context.Background(), identity, credDigest); err != nil {
		log.Printf("Upgrade external collector rejection durable write failed: %v", err)
	}
}

func (c *upgradeCollectorController) handleExternalRejectionRecorded(
	ctx context.Context,
	identity string,
	credDigest string,
) {
	if c == nil || c.manager == nil || identity == "" || credDigest == "" {
		return
	}
	activeCfg, ok := c.manager.ActiveRuntimeConfig()
	if !ok {
		return
	}
	activeCanonical, err := canonicalUpgradeUsageURL(activeCfg.CPAUpstreamURL)
	if err != nil || freshkeyapply.ExternalIdentity(activeCanonical) != identity {
		return
	}
	activeSecret := strings.TrimSpace(activeCfg.ManagementKey)

	c.mu.Lock()
	currentRunID := c.manager.StartCount()
	boundRunID := c.boundRunID
	boundTarget := c.boundTarget
	c.mu.Unlock()

	if boundRunID == currentRunID && boundTarget.targetIdentity == identity && boundTarget.credDigest != "" {
		if boundTarget.credDigest == credDigest {
			c.stopRunIfStillMatching(currentRunID, activeCfg)
			return
		}
		if security.EqualHMAC(activeSecret, boundTarget.committedSecret) {
			return
		}
	}

	if c.cfg.DB == nil || c.cfg.Protector == nil {
		return
	}
	checkCtx := ctx
	if checkCtx == nil {
		checkCtx = context.Background()
	}
	binding, hasBinding, err := upgradeoperation.New(c.cfg.DB).ExternalBinding(checkCtx)
	if err != nil || !hasBinding || binding.Identity != identity {
		return
	}
	dbDigest := envelopeDigest(strings.TrimSpace(binding.ManagementKey))
	dbSecret, decErr := c.cfg.Protector.UnprotectString(strings.TrimSpace(binding.ManagementKey))
	dbSecret = strings.TrimSpace(dbSecret)
	if dbDigest == credDigest {
		if decErr != nil || security.EqualHMAC(activeSecret, dbSecret) {
			c.stopRunIfStillMatching(currentRunID, activeCfg)
		}
		return
	}
	if decErr == nil && !security.EqualHMAC(activeSecret, dbSecret) {
		c.stopRunIfStillMatching(currentRunID, activeCfg)
	}
}

func (c *upgradeCollectorController) stopRunIfStillMatching(
	runID uint64,
	expectedCfg collector.RuntimeConfig,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	activeCfg, ok := c.manager.ActiveRunConfig(runID)
	if !ok || activeCfg != expectedCfg {
		return
	}
	if c.boundRunID == runID {
		c.boundRunID = 0
		c.boundTarget = verifiedUpgradeUsageTarget{}
	}
	c.manager.Stop()
}
