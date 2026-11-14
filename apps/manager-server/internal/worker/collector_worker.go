package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type CollectorWorker struct {
	cfg              config.Config
	store            *store.Store
	collectorService *collectorservice.Service
	gateMu           sync.RWMutex
	runtimeGate      func(ctx context.Context, cfg collectorpkg.RuntimeConfig) bool
}

func NewCollectorWorker(cfg config.Config, store *store.Store, collectorService *collectorservice.Service) *CollectorWorker {
	return &CollectorWorker{
		cfg:              cfg,
		store:            store,
		collectorService: collectorService,
	}
}

// SetRuntimeGate installs a startup gate evaluated before CollectorWorker
// starts collector.Manager for a resolved RuntimeConfig.
func (w *CollectorWorker) SetRuntimeGate(gate func(ctx context.Context, cfg collectorpkg.RuntimeConfig) bool) {
	if w == nil {
		return
	}
	w.gateMu.Lock()
	w.runtimeGate = gate
	w.gateMu.Unlock()
}

func (w *CollectorWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil || w.collectorService == nil {
		return
	}
	managerCfg, managerOK, err := w.store.LoadManagerConfig(ctx)
	if err != nil {
		log.Printf("load manager config: %v", err)
		return
	}
	if managerOK && !collectorservice.ManagerCollectorEnabled(managerCfg) {
		return
	}

	var runtimeCfg collectorpkg.RuntimeConfig
	var hasRuntimeCfg bool
	if w.cfg.CPAUpstreamURL != "" && w.cfg.ManagementKey != "" {
		runtimeCfg = collectorpkg.RuntimeConfig{
			CPAUpstreamURL: w.cfg.CPAUpstreamURL,
			ManagementKey:  w.cfg.ManagementKey,
			CollectorMode:  w.cfg.CollectorMode,
			Queue:          w.cfg.Queue,
			PopSide:        w.cfg.PopSide,
			BatchSize:      w.cfg.BatchSize,
			PollInterval:   w.cfg.PollInterval,
			TLSSkipVerify:  w.cfg.TLSSkipVerify,
		}
		hasRuntimeCfg = true
	} else if managerOK && managerCfg.CPAConnection.CPABaseURL != "" && managerCfg.CPAConnection.ManagementKey != "" {
		runtimeCfg = collectorservice.RuntimeConfigFromManagerConfigWithFallback(managerCfg, w.cfg)
		hasRuntimeCfg = true
	} else if setup, ok, err := w.store.LoadSetup(ctx); err == nil && ok {
		runtimeCfg = collectorpkg.RuntimeConfig{
			CPAUpstreamURL: setup.CPAUpstreamURL,
			ManagementKey:  setup.ManagementKey,
			CollectorMode:  w.cfg.CollectorMode,
			Queue:          setup.Queue,
			PopSide:        setup.PopSide,
			BatchSize:      w.cfg.BatchSize,
			PollInterval:   w.cfg.PollInterval,
			TLSSkipVerify:  w.cfg.TLSSkipVerify,
		}
		hasRuntimeCfg = true
	} else if err != nil {
		log.Printf("load setup: %v", err)
		return
	}
	if !hasRuntimeCfg {
		return
	}
	if !w.allowStart(ctx, runtimeCfg, managerCfg, managerOK) {
		return
	}
	_ = w.collectorService.StartRuntime(ctx, runtimeCfg)
}

func (w *CollectorWorker) allowStart(
	ctx context.Context,
	runtimeCfg collectorpkg.RuntimeConfig,
	managerCfg store.ManagerConfig,
	managerOK bool,
) bool {
	w.gateMu.RLock()
	gate := w.runtimeGate
	w.gateMu.RUnlock()
	if gate != nil {
		return gate(ctx, runtimeCfg)
	}
	return allowUpgradeStoreCollectorStart(ctx, w.cfg, w.store, runtimeCfg, managerCfg, managerOK)
}

const (
	workerUpgradeImportDestinationKey = "upgrade_cpa_import_destination_v1"
	workerUpgradeDestinationKey       = "upgrade_cpa_destination"
	workerExternalRejectionKey        = "fresh_external_rejection_v1"
	workerManagedDestinationClass     = "legacy_managed"
)

func allowUpgradeStoreCollectorStart(
	ctx context.Context,
	cfg config.Config,
	st *store.Store,
	runtimeCfg collectorpkg.RuntimeConfig,
	managerCfg store.ManagerConfig,
	managerOK bool,
) bool {
	if st == nil || st.SQLDB() == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	sqlDB := st.SQLDB()
	upgradeRepo := upgradeoperation.New(sqlDB)
	op, foundOp, err := upgradeRepo.Get(ctx)
	if err != nil {
		return false
	}
	binding, hasBinding, bindErr := upgradeRepo.ExternalBinding(ctx)
	if bindErr != nil {
		return false
	}
	importDestRaw, hasImportDest, err := readWorkerSettingRaw(ctx, sqlDB, workerUpgradeImportDestinationKey)
	if err != nil {
		return false
	}
	legacyDestRaw, hasLegacyDest, err := readWorkerSettingRaw(ctx, sqlDB, workerUpgradeDestinationKey)
	if err != nil {
		return false
	}
	if !foundOp && !hasBinding && !hasImportDest && !hasLegacyDest {
		return true
	}
	if !foundOp || op.Stage != model.UpgradeStageComplete {
		return false
	}
	if op.SourceKind != legacypreflight.SourceKindUpgradedLegacy ||
		strings.TrimSpace(op.SourceFingerprintBefore) == "" ||
		strings.TrimSpace(op.SourceFingerprintAfter) == "" ||
		strings.TrimSpace(op.BackupManifestSHA256) == "" ||
		len(op.DomainsDone) == 0 ||
		!op.RoutingCommitted ||
		strings.TrimSpace(op.ControlBasePath) == "" {
		return false
	}
	ev, err := op.VerificationEvidence()
	if err != nil || !ev.Complete() || strings.TrimSpace(ev.SourceFingerprint) != strings.TrimSpace(op.SourceFingerprintAfter) {
		return false
	}
	for _, domain := range ev.Domains {
		if !op.HasDomain(domain) {
			return false
		}
	}
	if !slices.Contains(ev.Domains, model.UpgradeDomainSourceCopy) ||
		!slices.Contains(ev.Domains, model.UpgradeDomainCPAConfig) {
		return false
	}
	if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, "", false, true); err != nil {
		return false
	}

	protector, ok := resolveWorkerUpgradeProtector(cfg)
	if !ok {
		return false
	}
	activeCanonical, err := canonicalWorkerURL(runtimeCfg.CPAUpstreamURL)
	if err != nil {
		return false
	}
	activeSecret := strings.TrimSpace(runtimeCfg.ManagementKey)
	if !cpa.ValidV8ManagementKey(activeSecret) {
		return false
	}
	setupCfg, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return false
	}
	persistedResolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setupCfg, setupOK)
	if err != nil {
		return false
	}

	var targetCanonicalURL, targetCommittedURL, targetSecret string
	switch ev.TargetMode {
	case model.UpgradeTargetExternal:
		if op.InferenceBasePath != "" || op.InferenceCommitted || op.SharedEntry {
			return false
		}
		if !hasBinding || !slices.Contains(ev.Domains, model.UpgradeDomainIdentity) {
			return false
		}
		fencedURL := upgradeservice.FencedExternalBaseURLForStore(ctx, sqlDB, protector, op, binding, hasBinding)
		if fencedURL == "" {
			return false
		}
		if strings.TrimSpace(ev.BindingIdentity) == "" ||
			ev.BindingIdentity != binding.Identity ||
			ev.BindingGeneration != binding.Generation ||
			ev.BindingObservedAtMS <= 0 ||
			ev.BindingObservedAtMS != binding.ObservedAtMS {
			return false
		}
		envelope := strings.TrimSpace(binding.ManagementKey)
		if !security.IsValidProtectedEnvelope(envelope) {
			return false
		}
		secret, err := protector.UnprotectString(envelope)
		if err != nil {
			return false
		}
		secret = strings.TrimSpace(secret)
		if !cpa.ValidV8ManagementKey(secret) {
			return false
		}
		credDigest := workerEnvelopeDigest(envelope)
		if refused, err := checkWorkerExternalRejectionRow(ctx, sqlDB, binding.Identity, credDigest); err != nil || refused {
			return false
		}
		targetCanonicalURL = fencedURL
		targetCommittedURL = fencedURL
		targetSecret = secret
	case model.UpgradeTargetManaged:
		if hasBinding {
			return false
		}
		if op.InferenceCommitted {
			if strings.TrimSpace(op.InferenceBasePath) == "" ||
				routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false) != nil {
				return false
			}
		} else if op.InferenceBasePath != "" {
			if routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false) != nil {
				return false
			}
		} else if op.SharedEntry {
			return false
		}
		committedURL, canonicalURL, secret, ok := verifyWorkerManagedDestination(importDestRaw, hasImportDest, legacyDestRaw, hasLegacyDest, protector)
		if !ok {
			return false
		}
		targetCommittedURL = committedURL
		targetCanonicalURL = canonicalURL
		targetSecret = secret
	default:
		return false
	}

	if activeCanonical != targetCanonicalURL || !security.EqualHMAC(activeSecret, targetSecret) {
		return false
	}
	if persistedResolution.Authority != managerconfig.LegacyConnectionAuthorityNone {
		persistedCanonical, err := canonicalWorkerURL(persistedResolution.Connection.BaseURL)
		if err != nil || persistedCanonical != targetCanonicalURL ||
			!security.EqualHMAC(strings.TrimSpace(persistedResolution.Connection.ManagementKey), targetSecret) {
			return false
		}
	} else if err := persistedResolution.ValidateRequestedLegacyConnection(managerconfig.LegacyConnection{
		BaseURL:       targetCommittedURL,
		ManagementKey: targetSecret,
	}); err != nil {
		return false
	}
	return true
}

func readWorkerSettingRaw(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `select value from settings where key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

func resolveWorkerUpgradeProtector(cfg config.Config) (*security.Protector, bool) {
	if raw := strings.TrimSpace(cfg.DataKey); raw != "" {
		keyBytes, _, err := security.LoadOrCreateDataKey(raw, "")
		if err != nil {
			return nil, false
		}
		p, err := security.NewProtector(keyBytes)
		return p, err == nil
	}
	keyPath := strings.TrimSpace(cfg.DataKeyPath)
	if keyPath == "" && strings.TrimSpace(cfg.DBPath) != "" {
		dir := filepath.Dir(cfg.DBPath)
		for _, candidate := range []string{filepath.Join(dir, "data.key"), filepath.Join(dir, ".data.key")} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				keyPath = candidate
				break
			}
		}
	}
	if keyPath == "" {
		return nil, false
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.IsDir() {
		return nil, false
	}
	keyBytes, _, err := security.LoadOrCreateDataKey("", keyPath)
	if err != nil {
		return nil, false
	}
	p, err := security.NewProtector(keyBytes)
	return p, err == nil
}

func canonicalWorkerURL(raw string) (string, error) {
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return identity.CanonicalExternalURL(normalized)
}

func workerEnvelopeDigest(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))
	return "envelope:sha256:" + hex.EncodeToString(sum[:])
}

func checkWorkerExternalRejectionRow(ctx context.Context, db *sql.DB, identityVal, credDigest string) (bool, error) {
	raw, found, err := readWorkerSettingRaw(ctx, db, workerExternalRejectionKey)
	if err != nil || !found {
		return false, err
	}
	var rec struct {
		Identity   string `json:"identity"`
		Credential string `json:"credential"`
	}
	if json.Unmarshal([]byte(raw), &rec) != nil || rec.Identity == "" || rec.Credential == "" {
		return false, cpa.ErrExternalRejectionUnavailable
	}
	return rec.Identity == identityVal && rec.Credential == credDigest, nil
}

func verifyWorkerManagedDestination(
	importRaw string,
	hasImport bool,
	legacyRaw string,
	hasLegacy bool,
	protector *security.Protector,
) (string, string, string, bool) {
	if !hasImport {
		return "", "", "", false
	}
	baseURL, canonicalURL, secret, serverFpr, ok := parseWorkerManagedDestination(importRaw, protector)
	if !ok {
		return "", "", "", false
	}
	if hasLegacy {
		var mirror struct {
			BaseURL           string `json:"baseUrl"`
			URLProved         bool   `json:"urlProved"`
			ManagementKey     string `json:"managementKey"`
			ServerFingerprint string `json:"serverFingerprint"`
			TerminalFailure   bool   `json:"terminalFailure,omitempty"`
		}
		if json.Unmarshal([]byte(legacyRaw), &mirror) != nil || !mirror.URLProved || mirror.TerminalFailure {
			return "", "", "", false
		}
		mirrorCanon, err := canonicalWorkerURL(mirror.BaseURL)
		if err != nil || mirrorCanon != canonicalURL || strings.TrimSpace(mirror.ServerFingerprint) != serverFpr {
			return "", "", "", false
		}
		mirrorSecret, err := protector.UnprotectString(strings.TrimSpace(mirror.ManagementKey))
		if err != nil || !security.EqualHMAC(strings.TrimSpace(mirrorSecret), secret) {
			return "", "", "", false
		}
	}
	return baseURL, canonicalURL, secret, true
}

func parseWorkerManagedDestination(raw string, protector *security.Protector) (string, string, string, string, bool) {
	var rec struct {
		BaseURL           string `json:"baseUrl"`
		URLProved         bool   `json:"urlProved"`
		ManagementKey     string `json:"managementKey"`
		ServerFingerprint string `json:"serverFingerprint"`
		OperationID       string `json:"operationId"`
		Attempt           int    `json:"attempt"`
		TerminalFailure   bool   `json:"terminalFailure,omitempty"`
		Intent            struct {
			ExpectedRuntimeIdentity   model.RuntimeIdentity   `json:"expectedRuntimeIdentity"`
			ExpectedRuntimeGeneration model.RuntimeGeneration `json:"expectedRuntimeGeneration"`
			ExpectedActiveArtifactID  model.RuntimeArtifactID `json:"expectedActiveArtifactId"`
			SourceIdentity            string                  `json:"sourceIdentity"`
			SourceClass               string                  `json:"sourceClass"`
			APIKeys                   []string                `json:"apiKeys"`
			PluginsEnabled            *bool                   `json:"pluginsEnabled,omitempty"`
		} `json:"intent"`
	}
	if json.Unmarshal([]byte(raw), &rec) != nil || !rec.URLProved || rec.TerminalFailure {
		return "", "", "", "", false
	}
	baseURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(rec.BaseURL))
	if err != nil || baseURL == "" {
		return "", "", "", "", false
	}
	canonicalURL, err := identity.CanonicalExternalURL(baseURL)
	if err != nil || canonicalURL == "" {
		return "", "", "", "", false
	}
	serverFingerprint := strings.TrimSpace(rec.ServerFingerprint)
	sourceIdentity := strings.TrimSpace(rec.Intent.SourceIdentity)
	operationID := strings.TrimSpace(rec.OperationID)
	runtimeIdentity := strings.TrimSpace(string(rec.Intent.ExpectedRuntimeIdentity))
	if !validWorkerExternalPrefixedDigest(serverFingerprint) ||
		!validWorkerExternalPrefixedDigest(sourceIdentity) ||
		strings.TrimSpace(rec.Intent.SourceClass) != workerManagedDestinationClass ||
		rec.Attempt < 1 ||
		operationID != workerManagedDestinationOperationID(sourceIdentity, rec.Attempt) ||
		runtimeIdentity == "" ||
		rec.Intent.ExpectedRuntimeGeneration == 0 ||
		len(rec.Intent.APIKeys) == 0 ||
		rec.Intent.PluginsEnabled == nil {
		return "", "", "", "", false
	}
	for _, k := range rec.Intent.APIKeys {
		if strings.TrimSpace(k) == "" {
			return "", "", "", "", false
		}
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil || rec.Intent.ExpectedActiveArtifactID != qualifiedArtifact {
		return "", "", "", "", false
	}
	envelope := strings.TrimSpace(rec.ManagementKey)
	if !security.IsValidProtectedEnvelope(envelope) || protector == nil {
		return "", "", "", "", false
	}
	secret, err := protector.UnprotectString(envelope)
	if err != nil {
		return "", "", "", "", false
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return "", "", "", "", false
	}
	return baseURL, canonicalURL, secret, serverFingerprint, true
}

func validWorkerExternalPrefixedDigest(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

func workerManagedDestinationOperationID(sourceIdentity string, attempt int) string {
	sum := sha256.Sum256([]byte(sourceIdentity))
	base := "upgrade-cpa-import:" + hex.EncodeToString(sum[:])[:32]
	if attempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s:attempt:%d", base, attempt)
}

func (w *CollectorWorker) Stop(ctx context.Context) {
	if err := w.collectorService.Stop(ctx); err != nil {
		log.Printf("stop collector: %v", err)
	}
}
