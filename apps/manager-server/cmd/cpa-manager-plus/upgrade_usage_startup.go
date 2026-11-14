package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

const (
	upgradeUsageStartProbeTimeout = 10 * time.Second
)

type upgradeUsageStartupConfig struct {
	DB                   *sql.DB
	Store                *store.Store
	Protector            *security.Protector
	Runtime              runtimeservice.RuntimeClient
	ManagerConfigService *managerconfig.Service
	Usage                *usageapp.Service
	Rejection            *upgradeExternalRejection
	CollectorWorker      *worker.CollectorWorker
}

// attachUpgradeUsageStartupObserver persists collection-start evidence on
// completed 1.x -> v2 Upgrade stores only when the active Collector run proves
// healthy queue polling while enabled and verified against the committed
// Managed or External CPA authority and enabled CPA usage queue, and shares the
// durable External credential refusal fence across CollectorWorker startup and
// active collector.Manager runs.
func attachUpgradeUsageStartupObserver(collectorManager *collector.Manager, cfg upgradeUsageStartupConfig) {
	if collectorManager == nil || cfg.DB == nil || cfg.Store == nil || cfg.Protector == nil || cfg.ManagerConfigService == nil || cfg.Usage == nil {
		return
	}
	newUpgradeCollectorController(collectorManager, cfg).attach()
}

func recordUpgradeCollectionStartIfProven(ctx context.Context, cfg upgradeUsageStartupConfig, runtimeCfg collector.RuntimeConfig) bool {
	proven, _ := proveUpgradeCollectionStart(ctx, nil, 0, cfg, runtimeCfg)
	return proven
}

type verifiedUpgradeUsageTarget struct {
	mode            string
	committedURL    string
	canonicalURL    string
	committedSecret string
	targetIdentity  string
	credDigest      string
}

func proveUpgradeCollectionStart(
	ctx context.Context,
	collectorManager *collector.Manager,
	runID uint64,
	cfg upgradeUsageStartupConfig,
	runtimeCfg collector.RuntimeConfig,
) (bool, error) {
	return proveUpgradeCollectionStartWithController(ctx, nil, collectorManager, runID, cfg, runtimeCfg)
}

func proveUpgradeCollectionStartWithController(
	ctx context.Context,
	ctrl *upgradeCollectorController,
	collectorManager *collector.Manager,
	runID uint64,
	cfg upgradeUsageStartupConfig,
	runtimeCfg collector.RuntimeConfig,
) (bool, error) {
	if cfg.DB == nil || cfg.Store == nil || cfg.Protector == nil || cfg.ManagerConfigService == nil || cfg.Usage == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if collectorManager != nil {
		activeCfg, ok := collectorManager.ActiveRunConfig(runID)
		if !ok || activeCfg != runtimeCfg {
			return false, nil
		}
	}

	checkCtx, cancel := context.WithTimeout(ctx, upgradeUsageStartProbeTimeout)
	defer cancel()

	target, ok, err := verifyUpgradeUsageTargetAndConfig(checkCtx, cfg, runtimeCfg)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if ctrl != nil {
		ctrl.bindActiveRun(runID, target)
	}

	probeCtx := checkCtx
	var recordedRejection atomic.Bool
	if target.mode == model.UpgradeTargetExternal && cfg.Rejection != nil {
		probeCtx = cpa.WithExternalHeaderObserver(checkCtx, func(statusCode int, _ bool) {
			if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
				if recordedRejection.CompareAndSwap(false, true) {
					if recErr := cfg.Rejection.record(context.WithoutCancel(checkCtx), target.targetIdentity, target.credDigest); recErr != nil {
						log.Printf("Upgrade external rejection durable write failed: %v", recErr)
					}
				}
			}
		})
	}

	usageCfg, err := cpa.FetchUsageConfig(probeCtx, target.committedURL, target.committedSecret)
	if err != nil {
		if target.mode == model.UpgradeTargetExternal && cfg.Rejection != nil && !recordedRejection.Load() &&
			(errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrManagementClientForbidden)) {
			if recErr := cfg.Rejection.record(context.WithoutCancel(checkCtx), target.targetIdentity, target.credDigest); recErr != nil {
				log.Printf("Upgrade external rejection durable write failed: %v", recErr)
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		log.Printf("Upgrade usage collection start probe failed: %v", err)
		return false, nil
	}
	if !usageCfg.UsageStatisticsEnabled || usageCfg.RedisUsageQueueRetentionSeconds <= 0 {
		log.Printf("Upgrade usage collection disabled on CPA; keeping collection start unproven")
		return false, nil
	}
	if err := checkCtx.Err(); err != nil {
		return false, err
	}
	if collectorManager != nil {
		activeCfg, ok := collectorManager.ActiveRunConfig(runID)
		if !ok || activeCfg != runtimeCfg {
			return false, nil
		}
	}

	postTarget, ok, err := verifyUpgradeUsageTargetAndConfig(checkCtx, cfg, runtimeCfg)
	if err != nil {
		return false, err
	}
	if !ok ||
		postTarget.mode != target.mode ||
		postTarget.canonicalURL != target.canonicalURL ||
		postTarget.targetIdentity != target.targetIdentity ||
		!security.EqualHMAC(postTarget.committedSecret, target.committedSecret) {
		return false, nil
	}
	if err := cfg.Usage.MarkCollectionStart(checkCtx); err != nil {
		return false, err
	}
	return true, nil
}

func verifyUpgradeUsageTargetAndConfig(
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

	activeCanonical, err := canonicalUpgradeUsageURL(runtimeCfg.CPAUpstreamURL)
	if err != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	activeSecret := strings.TrimSpace(runtimeCfg.ManagementKey)
	if !cpa.ValidV8ManagementKey(activeSecret) {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	resolvedCanonical, err := canonicalUpgradeUsageURL(resolvedCfg.CPAConnection.CPABaseURL)
	if err != nil {
		return verifiedUpgradeUsageTarget{}, false, nil
	}
	resolvedSecret := strings.TrimSpace(resolvedCfg.CPAConnection.ManagementKey)
	if !cpa.ValidV8ManagementKey(resolvedSecret) {
		return verifiedUpgradeUsageTarget{}, false, nil
	}

	var target verifiedUpgradeUsageTarget
	target.mode = ev.TargetMode
	switch ev.TargetMode {
	case model.UpgradeTargetManaged:
		rec, secret, targetID, mErr := verifyUpgradeManagedTargetForUsageStart(ctx, cfg)
		if mErr != nil {
			if isUpgradeTargetUnprovenError(mErr) {
				return verifiedUpgradeUsageTarget{}, false, nil
			}
			return verifiedUpgradeUsageTarget{}, false, mErr
		}
		target.committedURL = rec.BaseURL
		target.canonicalURL = rec.CanonicalURL
		target.committedSecret = secret
		target.targetIdentity = targetID
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
	if resolvedCanonical != target.canonicalURL || !security.EqualHMAC(resolvedSecret, target.committedSecret) {
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

func isUpgradeTargetUnprovenError(err error) bool {
	return errors.Is(err, freshkeyapply.ErrUnavailable) ||
		errors.Is(err, freshkeyapply.ErrExternalUnavailable) ||
		errors.Is(err, cpa.ErrExternalTargetMoved) ||
		errors.Is(err, cpa.ErrManagementRejected) ||
		errors.Is(err, upgradeoperation.ErrInvalidRow)
}

func canonicalUpgradeUsageURL(raw string) (string, error) {
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return identity.CanonicalExternalURL(normalized)
}

func verifyUpgradeManagedTargetForUsageStart(
	ctx context.Context,
	cfg upgradeUsageStartupConfig,
) (upgradeDestinationRecord, string, string, error) {
	rec, secret, err := loadVerifiedUpgradeManagedDestination(ctx, cfg.DB, cfg.Protector)
	if err != nil {
		return upgradeDestinationRecord{}, "", "", err
	}
	_, _, runtimeID, err := verifyUpgradeManagedLiveRuntime(ctx, cfg.Runtime, rec)
	if err != nil {
		return upgradeDestinationRecord{}, "", "", err
	}
	return rec, secret, runtimeID, nil
}

func verifyUpgradeExternalTargetForUsageStart(
	ctx context.Context,
	cfg upgradeUsageStartupConfig,
	upgradeRepo *upgradeoperation.Repository,
	op model.UpgradeOperation,
	ev model.UpgradeVerification,
) (string, string, string, string, string, error) {
	binding, hasBinding, err := upgradeRepo.ExternalBinding(ctx)
	if err != nil {
		return "", "", "", "", "", err
	}
	fencedURL := upgradeservice.FencedExternalBaseURLForStore(ctx, cfg.DB, cfg.Protector, op, binding, hasBinding)
	if fencedURL == "" {
		if hasBinding {
			return "", "", "", "", "", cpa.ErrExternalTargetMoved
		}
		return "", "", "", "", "", freshkeyapply.ErrExternalUnavailable
	}
	install, canonicalURL, credDigest, err := loadVerifiedUpgradeExternalBinding(ctx, upgradeRepo, cfg.Protector, ev)
	if err != nil {
		return "", "", "", "", "", err
	}
	if canonicalURL != fencedURL {
		return "", "", "", "", "", cpa.ErrExternalTargetMoved
	}
	if cfg.Rejection != nil {
		refused, checkErr := cfg.Rejection.check(ctx, install.Identity, credDigest)
		if checkErr != nil {
			if ctx != nil && ctx.Err() != nil {
				return "", "", "", "", "", ctx.Err()
			}
			return "", "", "", "", "", checkErr
		}
		if refused {
			return "", "", "", "", "", cpa.ErrManagementRejected
		}
	}
	return install.BaseURL, canonicalURL, install.ManagementKey, install.Identity, credDigest, nil
}
