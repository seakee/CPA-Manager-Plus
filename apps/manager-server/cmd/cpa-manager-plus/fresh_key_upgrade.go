package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

const (
	upgradeImportDestinationSettingKey = "upgrade_cpa_import_destination_v1"
	upgradeDestinationSettingKey       = "upgrade_cpa_destination"
	upgradeManagedKeysRegisteredKey    = "upgrade_managed_keys_registered_v1"
	upgradeManagedDestinationClass     = "legacy_managed"
)

// upgradeAPIKeysConfig supplies the real authorities of a completed 1.x -> v2
// Upgrade store without fabricating any FreshOperation checkpoint.
type upgradeAPIKeysConfig struct {
	DB              *sql.DB
	Protector       *security.Protector
	Runtime         runtimeservice.RuntimeClient
	SupervisorRoots []string
	Usage           *usageapp.Service
	WakeProjection  func()
}

type upgradeDestinationIntent struct {
	ExpectedRuntimeIdentity   model.RuntimeIdentity   `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration model.RuntimeGeneration `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  model.RuntimeArtifactID `json:"expectedActiveArtifactId"`
	SourceIdentity            string                  `json:"sourceIdentity"`
	SourceClass               string                  `json:"sourceClass"`
	APIKeys                   []string                `json:"apiKeys"`
	PluginsEnabled            *bool                   `json:"pluginsEnabled,omitempty"`
}

type upgradeDestinationRecord struct {
	BaseURL           string                   `json:"baseUrl"`
	CanonicalURL      string                   `json:"-"`
	URLProved         bool                     `json:"urlProved"`
	ManagementKey     string                   `json:"managementKey"`
	ServerFingerprint string                   `json:"serverFingerprint"`
	OperationID       string                   `json:"operationId"`
	Attempt           int                      `json:"attempt"`
	TerminalFailure   bool                     `json:"terminalFailure,omitempty"`
	Intent            upgradeDestinationIntent `json:"intent"`
}
type upgradeManagedRegistrationMarker struct {
	BaseURL           string `json:"baseUrl"`
	ServerFingerprint string `json:"serverFingerprint"`
	RuntimeIdentity   string `json:"runtimeIdentity,omitempty"`
}

type upgradeKeyWorkspace struct {
	db              *sql.DB
	protector       *security.Protector
	upgradeRepo     *upgradeoperation.Repository
	keyRepo         *keyrepo.Repository
	keys            *freshkeys.Service
	runtime         runtimeservice.RuntimeClient
	supervisorRoots []string
	usage           *usageapp.Service
	wakeProjection  func()

	mu                        sync.Mutex
	managedRegMu              sync.Mutex
	managedEndpoint           string
	managedBackfilledID       string
	managedAuthority          *freshkeyapply.Service
	externalAuthority         *freshkeyapply.Service
	rejection                 *upgradeExternalRejection
	credentialGate            externalCredentialGate
	provenanceMu              sync.RWMutex
	canonicalBound            bool
	expectedTargetMode        string
	expectedSourceFingerprint string
}

type upgradeExternalRejection struct {
	*externalRejection
	hookMu   sync.RWMutex
	onRecord func(ctx context.Context, identity, credDigest string)
	onStart  func(identity, credDigest, baseURL, managementKey string)
}

func newUpgradeExternalRejection(db *sql.DB) *upgradeExternalRejection {
	return &upgradeExternalRejection{
		externalRejection: newExternalRejection(db),
	}
}

func (r *upgradeExternalRejection) setOnRecord(fn func(ctx context.Context, identity, credDigest string)) {
	if r == nil {
		return
	}
	r.hookMu.Lock()
	r.onRecord = fn
	r.hookMu.Unlock()
}

func (r *upgradeExternalRejection) setOnStart(fn func(identity, credDigest, baseURL, managementKey string)) {
	if r == nil {
		return
	}
	r.hookMu.Lock()
	r.onStart = fn
	r.hookMu.Unlock()
}

func (r *upgradeExternalRejection) noteCollectorStart(identity, credDigest, baseURL, managementKey string) {
	if r == nil {
		return
	}
	r.hookMu.RLock()
	hook := r.onStart
	r.hookMu.RUnlock()
	if hook != nil {
		hook(identity, credDigest, baseURL, managementKey)
	}
}

func (r *upgradeExternalRejection) record(ctx context.Context, identity, credential string) error {
	if r == nil || r.externalRejection == nil || identity == "" || credential == "" {
		return nil
	}
	err := r.externalRejection.record(ctx, identity, credential)
	if r.isLocallyBlocked(identity, credential) {
		r.hookMu.RLock()
		hook := r.onRecord
		r.hookMu.RUnlock()
		if hook != nil {
			hook(context.WithoutCancel(ctx), identity, credential)
		}
	}
	return err
}

// newUpgradeAPIKeysHandler constructs the canonical `/api/v2/api-keys*` handler
// for completed 1.x -> v2 Upgrade stores. Key APIs and Gateway admission share
// this workspace's destination authority; closeFn retires its credential gate.
func newUpgradeAPIKeysHandler(cfg upgradeAPIKeysConfig) (*upgradeKeyWorkspace, func(), error) {
	if cfg.DB == nil || cfg.Protector == nil {
		return nil, nil, errors.New("upgrade key workspace requires database and secret protector")
	}
	upRepo := upgradeoperation.New(cfg.DB)
	kRepo := keyrepo.New(cfg.DB)
	w := &upgradeKeyWorkspace{
		db:              cfg.DB,
		protector:       cfg.Protector,
		upgradeRepo:     upRepo,
		keyRepo:         kRepo,
		runtime:         cfg.Runtime,
		supervisorRoots: append([]string(nil), cfg.SupervisorRoots...),
		usage:           cfg.Usage,
		wakeProjection:  cfg.WakeProjection,
		rejection:       newUpgradeExternalRejection(cfg.DB),
	}
	w.keys = freshkeys.New(kRepo, cfg.Protector, freshkeys.WithSourceRuntime(w.currentRuntimeIdentity))
	w.externalAuthority = freshkeyapply.NewExternal(kRepo, w.keys, w.resolveExternalInstall)
	w.backfillStartupBindings(context.Background())
	return w, func() { w.rejection.close() }, nil
}

func (u *upgradeKeyWorkspace) backfillStartupBindings(ctx context.Context) {
	ev, ok, err := u.loadCompletedVerdict(ctx)
	if err != nil || !ok || ev.TargetMode != model.UpgradeTargetExternal {
		return
	}
	install, _, err := u.loadVerifiedExternalInstall(ctx)
	if err != nil || install.Identity == "" {
		return
	}
	bound, err := u.keyRepo.BindFreshKeySources(ctx, install.Identity, time.Now().UnixMilli())
	if err != nil {
		log.Printf("Upgrade external key usage bindings unavailable: %v", err)
		return
	}
	if bound > 0 && u.wakeProjection != nil {
		u.wakeProjection()
	}
}

type upgradeRuntimeIdentityContextKey struct{}

func withUpgradeRuntimeIdentity(ctx context.Context, runtimeIdentity string) context.Context {
	if ctx == nil || runtimeIdentity == "" {
		return ctx
	}
	return context.WithValue(ctx, upgradeRuntimeIdentityContextKey{}, runtimeIdentity)
}

func upgradeRuntimeIdentityFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(upgradeRuntimeIdentityContextKey{}).(string)
	return id
}

func (u *upgradeKeyWorkspace) currentRuntimeIdentity(ctx context.Context) (string, error) {
	if cached := upgradeRuntimeIdentityFromContext(ctx); cached != "" {
		return cached, nil
	}
	ev, ok, err := u.loadCompletedVerdict(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("upgrade operation incomplete")
	}
	switch ev.TargetMode {
	case model.UpgradeTargetManaged:
		rec, _, err := u.loadManagedDestination(ctx)
		if err != nil {
			return "", err
		}
		_, _, runtimeID, err := verifyUpgradeManagedLiveRuntime(ctx, u.runtime, rec)
		if err != nil {
			return "", err
		}
		return runtimeID, nil
	case model.UpgradeTargetExternal:
		install, _, err := u.loadVerifiedExternalInstall(ctx)
		if err != nil {
			return "", err
		}
		return install.Identity, nil
	default:
		return "", errors.New("upgrade target mode unsupported")
	}
}

func loadVerifiedCompletedUpgradeOperation(
	ctx context.Context,
	upgradeRepo *upgradeoperation.Repository,
) (model.UpgradeOperation, model.UpgradeVerification, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, ctx.Err()
	}
	if upgradeRepo == nil {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, errors.New("upgrade repository unavailable")
	}
	op, found, err := upgradeRepo.Get(ctx)
	if err != nil {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, err
	}
	if !found || op.Stage != model.UpgradeStageComplete {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	if op.SourceKind != legacypreflight.SourceKindUpgradedLegacy ||
		strings.TrimSpace(op.SourceFingerprintBefore) == "" ||
		strings.TrimSpace(op.SourceFingerprintAfter) == "" ||
		strings.TrimSpace(op.BackupManifestSHA256) == "" ||
		len(op.DomainsDone) == 0 {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	ev, err := op.VerificationEvidence()
	if err != nil {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, err
	}
	if !ev.Complete() {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	if strings.TrimSpace(ev.SourceFingerprint) != strings.TrimSpace(op.SourceFingerprintAfter) {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	for _, domain := range ev.Domains {
		if !op.HasDomain(domain) {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
	}
	if !slices.Contains(ev.Domains, model.UpgradeDomainSourceCopy) ||
		!slices.Contains(ev.Domains, model.UpgradeDomainCPAConfig) {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	switch ev.TargetMode {
	case model.UpgradeTargetManaged:
		_, hasBinding, bindErr := upgradeRepo.ExternalBinding(ctx)
		if bindErr != nil {
			if errors.Is(bindErr, upgradeoperation.ErrInvalidRow) {
				return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
			}
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, bindErr
		}
		if hasBinding {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
	case model.UpgradeTargetExternal:
		if !slices.Contains(ev.Domains, model.UpgradeDomainIdentity) {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
	default:
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
	}
	return op, ev, true, nil
}

func (u *upgradeKeyWorkspace) bindCanonicalProvenance(mode string, sourceFingerprint string) {
	if u == nil {
		return
	}
	u.provenanceMu.Lock()
	u.canonicalBound = true
	u.expectedTargetMode = mode
	u.expectedSourceFingerprint = strings.TrimSpace(sourceFingerprint)
	u.provenanceMu.Unlock()
}

func upgradeOperationHasRoutingFields(op model.UpgradeOperation) bool {
	return op.RoutingCommitted ||
		strings.TrimSpace(op.ControlBasePath) != "" ||
		strings.TrimSpace(op.InferenceBasePath) != "" ||
		op.SharedEntry ||
		op.InferenceCommitted
}

func (u *upgradeKeyWorkspace) loadVerifiedOperation(ctx context.Context) (model.UpgradeOperation, model.UpgradeVerification, bool, error) {
	op, ev, ok, err := loadVerifiedCompletedUpgradeOperation(ctx, u.upgradeRepo)
	if err != nil || !ok {
		return model.UpgradeOperation{}, model.UpgradeVerification{}, false, err
	}
	u.provenanceMu.RLock()
	bound := u.canonicalBound
	expectedMode := u.expectedTargetMode
	expectedFpr := u.expectedSourceFingerprint
	u.provenanceMu.RUnlock()
	if bound {
		if ev.TargetMode != expectedMode || strings.TrimSpace(ev.SourceFingerprint) != expectedFpr {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
		if validateCompletedUpgradeRoutingRow(op, ev.TargetMode) != nil {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
	} else if upgradeOperationHasRoutingFields(op) {
		if validateCompletedUpgradeRoutingRow(op, ev.TargetMode) != nil {
			return model.UpgradeOperation{}, model.UpgradeVerification{}, false, nil
		}
	}
	return op, ev, true, nil
}

func (u *upgradeKeyWorkspace) loadCompletedVerdict(ctx context.Context) (model.UpgradeVerification, bool, error) {
	_, ev, ok, err := u.loadVerifiedOperation(ctx)
	return ev, ok, err
}

func (u *upgradeKeyWorkspace) gatewayAdmitted(ctx context.Context) bool {
	ev, complete, err := u.loadCompletedVerdict(ctx)
	if err != nil || !complete || ev.TargetMode != model.UpgradeTargetManaged {
		return false
	}
	authority, _, _, err := u.resolveManagedAuthority(ctx)
	return err == nil && authority.DefaultInstallCPAAuthorityProven(ctx)
}

func validUpgradeManagedPrefixedDigest(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

func upgradeManagedDestinationOperationID(sourceIdentity string, attempt int) string {
	sum := sha256.Sum256([]byte(sourceIdentity))
	base := "upgrade-cpa-import:" + hex.EncodeToString(sum[:])[:32]
	if attempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s:attempt:%d", base, attempt)
}

func readUpgradeSettingRaw(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return "", false, ctx.Err()
	}
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

func parseFullUpgradeManagedDestination(raw string, protector *security.Protector) (upgradeDestinationRecord, string, error) {
	var rec upgradeDestinationRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || !rec.URLProved || rec.TerminalFailure {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	baseURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(rec.BaseURL))
	if err != nil || baseURL == "" {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	canonicalURL, err := identity.CanonicalExternalURL(baseURL)
	if err != nil || canonicalURL == "" {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	serverFingerprint := strings.TrimSpace(rec.ServerFingerprint)
	sourceIdentity := strings.TrimSpace(rec.Intent.SourceIdentity)
	operationID := strings.TrimSpace(rec.OperationID)
	runtimeIdentity := model.RuntimeIdentity(strings.TrimSpace(string(rec.Intent.ExpectedRuntimeIdentity)))
	if !validUpgradeManagedPrefixedDigest(serverFingerprint) ||
		!validUpgradeManagedPrefixedDigest(sourceIdentity) ||
		strings.TrimSpace(rec.Intent.SourceClass) != upgradeManagedDestinationClass ||
		rec.Attempt < 1 ||
		operationID != upgradeManagedDestinationOperationID(sourceIdentity, rec.Attempt) ||
		runtimeIdentity == "" ||
		rec.Intent.ExpectedRuntimeGeneration == 0 ||
		len(rec.Intent.APIKeys) == 0 ||
		rec.Intent.PluginsEnabled == nil {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	for _, k := range rec.Intent.APIKeys {
		if strings.TrimSpace(k) == "" {
			return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
		}
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil || rec.Intent.ExpectedActiveArtifactID != qualifiedArtifact {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	envelope := strings.TrimSpace(rec.ManagementKey)
	if !security.IsValidProtectedEnvelope(envelope) || protector == nil {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	secret, err := protector.UnprotectString(envelope)
	if err != nil {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	rec.BaseURL = baseURL
	rec.CanonicalURL = canonicalURL
	rec.ServerFingerprint = serverFingerprint
	rec.OperationID = operationID
	rec.Intent.SourceIdentity = sourceIdentity
	rec.Intent.SourceClass = upgradeManagedDestinationClass
	rec.Intent.ExpectedRuntimeIdentity = runtimeIdentity
	return rec, secret, nil
}

func legacyUpgradeManagedDestinationAligned(
	primary upgradeDestinationRecord,
	primarySecret string,
	rawLegacy string,
	protector *security.Protector,
) bool {
	var leg upgradeDestinationRecord
	if err := json.Unmarshal([]byte(rawLegacy), &leg); err != nil || !leg.URLProved || leg.TerminalFailure {
		return false
	}
	legBase, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(leg.BaseURL))
	if err != nil || legBase == "" {
		return false
	}
	legCanon, err := identity.CanonicalExternalURL(legBase)
	if err != nil || legCanon != primary.CanonicalURL {
		return false
	}
	if strings.TrimSpace(leg.ServerFingerprint) != primary.ServerFingerprint {
		return false
	}
	envelope := strings.TrimSpace(leg.ManagementKey)
	if !security.IsValidProtectedEnvelope(envelope) || protector == nil {
		return false
	}
	legSecret, err := protector.UnprotectString(envelope)
	if err != nil || !security.EqualHMAC(strings.TrimSpace(legSecret), primarySecret) {
		return false
	}
	if v := strings.TrimSpace(leg.OperationID); v != "" && v != primary.OperationID {
		return false
	}
	if leg.Attempt != 0 && leg.Attempt != primary.Attempt {
		return false
	}
	if v := strings.TrimSpace(leg.Intent.SourceIdentity); v != "" && v != primary.Intent.SourceIdentity {
		return false
	}
	if v := strings.TrimSpace(leg.Intent.SourceClass); v != "" && v != primary.Intent.SourceClass {
		return false
	}
	if v := model.RuntimeIdentity(strings.TrimSpace(string(leg.Intent.ExpectedRuntimeIdentity))); v != "" && v != primary.Intent.ExpectedRuntimeIdentity {
		return false
	}
	if leg.Intent.ExpectedRuntimeGeneration != 0 && leg.Intent.ExpectedRuntimeGeneration != primary.Intent.ExpectedRuntimeGeneration {
		return false
	}
	if v := model.RuntimeArtifactID(strings.TrimSpace(string(leg.Intent.ExpectedActiveArtifactID))); v != "" && v != primary.Intent.ExpectedActiveArtifactID {
		return false
	}
	if leg.Intent.PluginsEnabled != nil && *leg.Intent.PluginsEnabled != *primary.Intent.PluginsEnabled {
		return false
	}
	if len(leg.Intent.APIKeys) > 0 && !slices.Equal(leg.Intent.APIKeys, primary.Intent.APIKeys) {
		return false
	}
	return true
}

func loadVerifiedUpgradeManagedDestination(
	ctx context.Context,
	db *sql.DB,
	protector *security.Protector,
) (upgradeDestinationRecord, string, error) {
	if ctx != nil && ctx.Err() != nil {
		return upgradeDestinationRecord{}, "", ctx.Err()
	}
	if db == nil || protector == nil {
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
	rawImport, importFound, err := readUpgradeSettingRaw(ctx, db, upgradeImportDestinationSettingKey)
	if err != nil {
		return upgradeDestinationRecord{}, "", err
	}
	rawLegacy, legacyFound, err := readUpgradeSettingRaw(ctx, db, upgradeDestinationSettingKey)
	if err != nil {
		return upgradeDestinationRecord{}, "", err
	}
	switch {
	case importFound && legacyFound:
		rec, secret, err := parseFullUpgradeManagedDestination(rawImport, protector)
		if err != nil {
			return upgradeDestinationRecord{}, "", err
		}
		if !legacyUpgradeManagedDestinationAligned(rec, secret, rawLegacy, protector) {
			return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
		}
		return rec, secret, nil
	case importFound:
		return parseFullUpgradeManagedDestination(rawImport, protector)
	case legacyFound:
		return parseFullUpgradeManagedDestination(rawLegacy, protector)
	default:
		return upgradeDestinationRecord{}, "", freshkeyapply.ErrUnavailable
	}
}

func verifyUpgradeManagedLiveRuntime(
	ctx context.Context,
	runtime runtimeservice.RuntimeClient,
	rec upgradeDestinationRecord,
) (model.RuntimeObservedStatus, uint64, string, error) {
	if ctx != nil && ctx.Err() != nil {
		return model.RuntimeObservedStatus{}, 0, "", ctx.Err()
	}
	if runtime == nil {
		return model.RuntimeObservedStatus{}, 0, "", freshkeyapply.ErrUnavailable
	}
	obs, err := runtime.Status(ctx)
	if ctx != nil && ctx.Err() != nil {
		return model.RuntimeObservedStatus{}, 0, "", ctx.Err()
	}
	if err != nil ||
		obs.Validate() != nil ||
		obs.ProtocolVersion != "v1" ||
		obs.State != model.RuntimeStateReady ||
		obs.Identity == "" ||
		obs.Identity != rec.Intent.ExpectedRuntimeIdentity ||
		obs.Generation == 0 ||
		obs.ActiveGatewayArtifact == nil ||
		obs.ActiveGatewayArtifact.ArtifactID != rec.Intent.ExpectedActiveArtifactID {
		return model.RuntimeObservedStatus{}, 0, "", freshkeyapply.ErrUnavailable
	}
	epoch := uint64(rec.Intent.ExpectedRuntimeGeneration)
	if epoch == 0 {
		return model.RuntimeObservedStatus{}, 0, "", freshkeyapply.ErrUnavailable
	}
	return obs, epoch, string(obs.Identity), nil
}

func (u *upgradeKeyWorkspace) loadManagedDestination(ctx context.Context) (upgradeDestinationRecord, string, error) {
	return loadVerifiedUpgradeManagedDestination(ctx, u.db, u.protector)
}

func (u *upgradeKeyWorkspace) resolveManagedAuthority(ctx context.Context) (*freshkeyapply.Service, uint64, string, error) {
	ev, ok, err := u.loadCompletedVerdict(ctx)
	if err != nil {
		return nil, 0, "", err
	}
	if !ok || ev.TargetMode != model.UpgradeTargetManaged {
		return nil, 0, "", freshkeyapply.ErrUnavailable
	}
	rec, secret, err := u.loadManagedDestination(ctx)
	if err != nil {
		return nil, 0, "", err
	}
	if len(secret) != 64 {
		return nil, 0, "", freshkeyapply.ErrUnavailable
	}
	_, epoch, runtimeID, err := verifyUpgradeManagedLiveRuntime(ctx, u.runtime, rec)
	if err != nil {
		return nil, 0, "", err
	}
	if err := u.ensureManagedKeysRegistered(ctx, rec, runtimeID); err != nil {
		return nil, 0, "", err
	}
	u.mu.Lock()
	needBackfill := u.managedBackfilledID != runtimeID
	u.mu.Unlock()
	if needBackfill {
		bound, err := u.keyRepo.BindFreshKeySources(ctx, runtimeID, time.Now().UnixMilli())
		if err != nil {
			return nil, 0, "", err
		}
		u.mu.Lock()
		u.managedBackfilledID = runtimeID
		u.mu.Unlock()
		if bound > 0 && u.wakeProjection != nil {
			u.wakeProjection()
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.managedAuthority == nil || u.managedEndpoint != rec.BaseURL {
		auth := freshkeyapply.NewManaged(u.keyRepo, u.keys, u.runtime, rec.BaseURL, func(callCtx context.Context) (freshkeyapply.ManagedInstall, error) {
			curEv, curOK, verdictErr := u.loadCompletedVerdict(callCtx)
			if verdictErr != nil {
				return freshkeyapply.ManagedInstall{}, verdictErr
			}
			if !curOK || curEv.TargetMode != model.UpgradeTargetManaged {
				return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
			}
			curRec, curSecret, loadErr := u.loadManagedDestination(callCtx)
			if loadErr != nil {
				return freshkeyapply.ManagedInstall{}, loadErr
			}
			if len(curSecret) != 64 {
				return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
			}
			status, curEpoch, curRuntimeID, statusErr := verifyUpgradeManagedLiveRuntime(callCtx, u.runtime, curRec)
			if statusErr != nil {
				return freshkeyapply.ManagedInstall{}, statusErr
			}
			return freshkeyapply.ManagedInstall{
				RunID:           "upgrade_managed:" + curRec.OperationID + ":" + curRec.ServerFingerprint,
				Generation:      strconv.FormatUint(curEpoch, 10),
				Identity:        curRuntimeID,
				ArtifactID:      string(status.ActiveGatewayArtifact.ArtifactID),
				BaseURL:         curRec.BaseURL,
				ManagementKey:   curSecret,
				Strategy:        freshkeyapply.StrategyUpgradedManaged,
				ProductComplete: true,
			}, nil
		})
		if len(u.supervisorRoots) > 0 {
			auth.SetTrustedSupervisorRoots(u.supervisorRoots)
		}
		u.managedEndpoint = rec.BaseURL
		u.managedAuthority = auth
	}
	return u.managedAuthority, epoch, runtimeID, nil
}

type upgradeSettingsQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (u *upgradeKeyWorkspace) managedMarkerMatches(ctx context.Context, q upgradeSettingsQueryer, rec upgradeDestinationRecord, runtimeIdentity string) (bool, error) {
	var rawMarker string
	err := q.QueryRowContext(ctx, `select value from settings where key = ?`, upgradeManagedKeysRegisteredKey).Scan(&rawMarker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marker upgradeManagedRegistrationMarker
	if json.Unmarshal([]byte(rawMarker), &marker) != nil {
		return false, errors.New("invalid upgrade managed registration marker")
	}
	return marker.BaseURL == rec.BaseURL &&
		marker.ServerFingerprint == rec.ServerFingerprint &&
		marker.RuntimeIdentity == runtimeIdentity &&
		runtimeIdentity != "", nil
}

// ensureManagedKeysRegistered stages the destination CPA's imported 1.x client
// keys once per destination and runtime identity after Upgrade Managed completes,
// reusing any pre-existing canonical APIKeyID in gateway_api_key_identities (via
// gateway_api_key_source_bindings) so migrated 1.x identities and usage history
// survive intact. Once recorded, later out-of-band CPA edits still fail closed
// with key_operation_source_conflict.
func (u *upgradeKeyWorkspace) ensureManagedKeysRegistered(ctx context.Context, rec upgradeDestinationRecord, runtimeIdentity string) error {
	if runtimeIdentity == "" {
		return freshkeyapply.ErrUnavailable
	}
	u.managedRegMu.Lock()
	regLocked := true
	defer func() {
		if regLocked {
			u.managedRegMu.Unlock()
		}
	}()

	if matched, err := u.managedMarkerMatches(ctx, u.db, rec, runtimeIdentity); err != nil {
		return err
	} else if matched {
		return nil
	}

	rec, secret, err := u.loadManagedDestination(ctx)
	if err != nil {
		return err
	}
	liveKeys, err := cpa.ProveAdoptedDestination(ctx, rec.BaseURL, secret, u.supervisorRoots...)
	if err != nil {
		return err
	}
	candidates, err := u.keys.PrepareAdopted(liveKeys)
	if err != nil {
		return err
	}

	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if matched, err := u.managedMarkerMatches(ctx, tx, rec, runtimeIdentity); err != nil {
		return err
	} else if matched {
		return nil
	}

	if _, err := u.keyRepo.StageAdoptedTx(ctx, tx, candidates, runtimeIdentity); err != nil {
		return err
	}
	if _, err := u.keyRepo.BindFreshKeySourcesTx(ctx, tx, runtimeIdentity, time.Now().UnixMilli()); err != nil {
		return err
	}
	markerBytes, err := json.Marshal(upgradeManagedRegistrationMarker{
		BaseURL:           rec.BaseURL,
		ServerFingerprint: rec.ServerFingerprint,
		RuntimeIdentity:   runtimeIdentity,
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		 on conflict (key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		upgradeManagedKeysRegisteredKey, string(markerBytes), time.Now().UnixMilli(),
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	u.managedRegMu.Unlock()
	regLocked = false

	if u.wakeProjection != nil {
		u.wakeProjection()
	}
	return nil
}

func loadVerifiedUpgradeExternalBinding(
	ctx context.Context,
	upgradeRepo *upgradeoperation.Repository,
	protector *security.Protector,
	ev model.UpgradeVerification,
) (freshkeyapply.ExternalInstall, string, string, error) {
	if ctx != nil && ctx.Err() != nil {
		return freshkeyapply.ExternalInstall{}, "", "", ctx.Err()
	}
	if upgradeRepo == nil {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	binding, found, err := upgradeRepo.ExternalBinding(ctx)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", "", err
	}
	if !found || binding.ObservedAtMS <= 0 {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	if binding.Identity != ev.BindingIdentity ||
		binding.Generation != ev.BindingGeneration ||
		binding.ObservedAtMS != ev.BindingObservedAtMS {
		return freshkeyapply.ExternalInstall{}, "", "", cpa.ErrExternalTargetMoved
	}
	normalizedURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(binding.BaseURL))
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	canonicalURL, err := identity.CanonicalExternalURL(normalizedURL)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	if binding.Identity != binding.Generation || freshkeyapply.ExternalIdentity(canonicalURL) != binding.Identity {
		return freshkeyapply.ExternalInstall{}, "", "", cpa.ErrExternalTargetMoved
	}
	envelope := strings.TrimSpace(binding.ManagementKey)
	if !security.IsValidProtectedEnvelope(envelope) || protector == nil {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	secret, err := protector.UnprotectString(envelope)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return freshkeyapply.ExternalInstall{}, "", "", freshkeyapply.ErrExternalUnavailable
	}
	return freshkeyapply.ExternalInstall{
		Identity:      binding.Identity,
		Checkpoint:    uint64(binding.ObservedAtMS),
		BaseURL:       canonicalURL,
		ManagementKey: secret,
	}, canonicalURL, envelopeDigest(envelope), nil
}

func checkUpgradeSettingsReadable(ctx context.Context, db *sql.DB) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if db == nil {
		return errors.New("upgrade database unavailable")
	}
	for _, key := range []string{"manager_config_v1", "setup"} {
		var raw string
		err := db.QueryRowContext(ctx, `select value from settings where key = ?`, key).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (u *upgradeKeyWorkspace) loadVerifiedExternalInstall(ctx context.Context) (freshkeyapply.ExternalInstall, string, error) {
	op, ev, ok, err := u.loadVerifiedOperation(ctx)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", err
	}
	if !ok || ev.TargetMode != model.UpgradeTargetExternal {
		return freshkeyapply.ExternalInstall{}, "", freshkeyapply.ErrExternalUnavailable
	}
	install, canonicalURL, credDigest, err := loadVerifiedUpgradeExternalBinding(ctx, u.upgradeRepo, u.protector, ev)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", err
	}
	binding, found, bindErr := u.upgradeRepo.ExternalBinding(ctx)
	if bindErr != nil {
		return freshkeyapply.ExternalInstall{}, "", bindErr
	}
	if !found {
		return freshkeyapply.ExternalInstall{}, "", freshkeyapply.ErrExternalUnavailable
	}
	if err := checkUpgradeSettingsReadable(ctx, u.db); err != nil {
		return freshkeyapply.ExternalInstall{}, "", err
	}
	fencedURL := upgradeservice.FencedExternalBaseURLForStore(ctx, u.db, u.protector, op, binding, true)
	if fencedURL == "" || fencedURL != canonicalURL {
		if ctx != nil && ctx.Err() != nil {
			return freshkeyapply.ExternalInstall{}, "", ctx.Err()
		}
		return freshkeyapply.ExternalInstall{}, "", cpa.ErrExternalTargetMoved
	}
	return install, credDigest, nil
}

func (u *upgradeKeyWorkspace) loadExternalBindingGrant(ctx context.Context) (externalReadGrant, freshkeyapply.ExternalInstall, error) {
	install, credDigest, err := u.loadVerifiedExternalInstall(ctx)
	if err != nil {
		return externalReadGrant{}, freshkeyapply.ExternalInstall{}, err
	}
	refused, checkErr := u.rejection.check(ctx, install.Identity, credDigest)
	if checkErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return externalReadGrant{}, freshkeyapply.ExternalInstall{}, ctx.Err()
		}
		return externalReadGrant{}, freshkeyapply.ExternalInstall{}, errExternalRejectionUnavailable
	}
	return externalReadGrant{
		Identity:   install.Identity,
		Credential: credDigest,
		Refused:    refused,
	}, install, nil
}

func (u *upgradeKeyWorkspace) resolveExternalInstall(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
	grant, install, err := u.loadExternalBindingGrant(ctx)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, err
	}
	if grant.Refused {
		return freshkeyapply.ExternalInstall{}, cpa.ErrManagementRejected
	}
	return install, nil
}
func (u *upgradeKeyWorkspace) withExternalGate(ctx context.Context) context.Context {
	return cpa.WithExternalSavedCredentialGate(ctx, func(
		callCtx context.Context,
		call func(requestCtx context.Context, target cpa.ExternalSavedCredentialTarget) error,
	) error {
		if !hasUpgradeExternalHold(callCtx) {
			if !u.credentialGate.acquire(callCtx) {
				return errExternalCredentialWaitCancelled
			}
			defer u.credentialGate.release()
		}
		grant, install, err := u.loadExternalBindingGrant(callCtx)
		if err != nil {
			return err
		}
		if grant.Refused {
			return cpa.ErrManagementRejected
		}
		var recorded atomic.Bool
		obsCtx := cpa.WithExternalHeaderObserver(callCtx, func(statusCode int, _ bool) {
			if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
				if recorded.CompareAndSwap(false, true) {
					if recErr := u.rejection.record(context.WithoutCancel(callCtx), grant.Identity, grant.Credential); recErr != nil {
						log.Printf("Upgrade external rejection durable write failed: %v", recErr)
					}
				}
			}
		})
		runErr := call(obsCtx, cpa.ExternalSavedCredentialTarget{
			BaseURL:       install.BaseURL,
			ManagementKey: install.ManagementKey,
		})
		if runErr != nil && !recorded.Load() && errors.Is(runErr, cpa.ErrManagementRejected) {
			if recErr := u.rejection.record(context.WithoutCancel(callCtx), grant.Identity, grant.Credential); recErr != nil {
				log.Printf("Upgrade external rejection durable write failed: %v", recErr)
			}
		}
		return runErr
	})
}

func (u *upgradeKeyWorkspace) precheckExternalInstall(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
	if !u.credentialGate.acquire(ctx) {
		return freshkeyapply.ExternalInstall{}, errExternalCredentialWaitCancelled
	}
	defer u.credentialGate.release()
	grant, install, err := u.loadExternalBindingGrant(ctx)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, err
	}
	if grant.Refused {
		return freshkeyapply.ExternalInstall{}, cpa.ErrManagementRejected
	}
	return install, nil
}

func (u *upgradeKeyWorkspace) precheckExternal(ctx context.Context) (uint64, error) {
	install, err := u.precheckExternalInstall(ctx)
	if err != nil {
		return 0, err
	}
	return install.Checkpoint, nil
}

func (u *upgradeKeyWorkspace) serveHTTP(w http.ResponseWriter, r *http.Request, resource string) {
	ev, ok, verdictErr := u.loadCompletedVerdict(r.Context())
	if verdictErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if strings.HasPrefix(resource, "api-keys/") && strings.Count(resource, "/") == 1 && r.Method == http.MethodPut {
		u.serveRenameKey(w, r, ev, strings.TrimPrefix(resource, "api-keys/"))
		return
	}
	var (
		authority  *freshkeyapply.Service
		generation uint64
		runtimeID  string
		isExternal = ev.TargetMode == model.UpgradeTargetExternal
	)
	if isExternal {
		authority = u.externalAuthority
	} else if ev.TargetMode == model.UpgradeTargetManaged {
		var err error
		authority, generation, runtimeID, err = u.resolveManagedAuthority(r.Context())
		if err != nil {
			if r.Method == http.MethodGet && resource == "api-keys" && errors.Is(err, freshkeyapply.ErrUnavailable) {
				keys, listErr := u.keys.List(r.Context())
				if listErr != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				for i := range keys {
					keys[i].RuntimeState = "effect_unknown"
				}
				writeFreshJSON(w, struct {
					Keys                []freshkeys.Key `json:"keys"`
					GatewayPending      bool            `json:"gatewayPending"`
					RevocationAvailable bool            `json:"revocationAvailable"`
					PresenceUnverified  bool            `json:"presenceUnverified"`
				}{keys, true, false, true})
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	} else {
		w.WriteHeader(http.StatusConflict)
		return
	}

	switch {
	case strings.HasPrefix(resource, "api-keys/operations/"):
		u.serveKeyOperation(w, r, authority, isExternal, generation, resource)
	case resource == "api-keys/usage-summary" && r.Method == http.MethodGet:
		if u.usage == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ids, err := u.keys.IdentityIDs(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		summary, err := u.usage.Summary(r.Context(), ids)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeFreshJSON(w, summary)
	case strings.HasSuffix(resource, "/usage") && strings.Count(resource, "/") == 2 && r.Method == http.MethodGet:
		if u.usage == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rawID := strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/usage")
		key, err := u.keys.Detail(r.Context(), rawID)
		switch {
		case errors.Is(err, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
			return
		case errors.Is(err, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fromMS, toMS, err := usageWindow(r.URL.Query())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		detail, err := u.usage.Detail(r.Context(), identity.APIKeyID(key.ID), fromMS, toMS)
		if errors.Is(err, usageapp.ErrWindow) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeFreshJSON(w, detail)
	case r.Method == http.MethodGet && strings.Count(resource, "/") == 4:
		if parts := strings.Split(resource, "/"); parts[0] == "api-keys" && parts[2] == "revocation" {
			u.servePreflight(w, r, authority, isExternal, generation, parts[1], parts[3], parts[4])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && strings.Count(resource, "/") == 3:
		if parts := strings.Split(resource, "/"); parts[0] == "api-keys" && parts[2] == "revocation" {
			u.servePreflight(w, r, authority, isExternal, generation, parts[1], "", parts[3])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(resource, "/revocation") && strings.Count(resource, "/") == 2:
		u.servePrepare(w, r, authority, isExternal, generation, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/revocation"))
	case strings.HasSuffix(resource, "/secret") && strings.Count(resource, "/") == 2 && r.Method == http.MethodPost:
		serveClientKeySecret(w, r, u.keys, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/secret"))
	case resource == "api-keys" && r.Method == http.MethodGet:
		u.serveList(w, r, authority, isExternal, generation)
	case strings.HasPrefix(resource, "api-keys/requests/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(resource, "api-keys/requests/")
		key, err := u.keys.Lookup(r.Context(), id)
		switch {
		case errors.Is(err, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
			return
		case errors.Is(err, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		observed, ok := u.observeKeyForRead(w, r.Context(), authority, isExternal, key, generation)
		if !ok {
			return
		}
		writeFreshJSON(w, observed)
	case strings.HasPrefix(resource, "api-keys/") && strings.Count(resource, "/") == 1 && r.Method == http.MethodGet:
		id := strings.TrimPrefix(resource, "api-keys/")
		key, err := u.keys.Detail(r.Context(), id)
		switch {
		case errors.Is(err, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
			return
		case errors.Is(err, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case errors.Is(err, freshkeys.ErrConflict):
			w.WriteHeader(http.StatusConflict)
			return
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		observed, ok := u.observeKeyForRead(w, r.Context(), authority, isExternal, key, generation)
		if !ok {
			return
		}
		writeFreshJSON(w, observed)
	case resource == "api-keys" && r.Method == http.MethodPost:
		u.serveCreate(w, r, authority, isExternal, generation, runtimeID)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (u *upgradeKeyWorkspace) serveList(w http.ResponseWriter, r *http.Request, authority *freshkeyapply.Service, isExternal bool, generation uint64) {
	ctx := r.Context()
	if isExternal {
		preGen, preErr := u.precheckExternal(ctx)
		switch {
		case errors.Is(preErr, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return
		case errors.Is(preErr, errExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
			return
		case errors.Is(preErr, cpa.ErrExternalTargetMoved):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{freshconfig.ReasonTargetMoved})
			return
		case errors.Is(preErr, cpa.ErrManagementClientForbidden):
			writeExternalManagementForbidden(w)
			return
		case errors.Is(preErr, cpa.ErrManagementRejected):
			writeExternalManagementRejected(w)
			return
		case preErr != nil && !errors.Is(preErr, freshkeyapply.ErrExternalUnavailable) && !errors.Is(preErr, freshkeyapply.ErrUnavailable):
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case preErr == nil:
			generation = preGen
		}
		ctx = u.withExternalGate(ctx)
	}
	keys, unverified, err := authority.ObservedKeyList(ctx, generation)
	if err != nil {
		switch {
		case errors.Is(err, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return
		case errors.Is(err, cpa.ErrExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
			return
		case errors.Is(err, cpa.ErrExternalTargetMoved):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{freshconfig.ReasonTargetMoved})
			return
		case errors.Is(err, cpa.ErrManagementClientForbidden):
			writeExternalManagementForbidden(w)
			return
		case errors.Is(err, cpa.ErrManagementRejected):
			writeExternalManagementRejected(w)
			return
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	gatewayPending := false
	if !isExternal {
		admitCtx, cancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
		gatewayPending = !authority.DefaultInstallCPAAuthorityProven(admitCtx)
		cancel()
	}
	writeFreshJSON(w, struct {
		Keys                []freshkeys.Key `json:"keys"`
		GatewayPending      bool            `json:"gatewayPending"`
		RevocationAvailable bool            `json:"revocationAvailable"`
		PresenceUnverified  bool            `json:"presenceUnverified"`
	}{keys, gatewayPending, authority.KeyOperationsEnabled(), unverified})
}

func (u *upgradeKeyWorkspace) serveRenameKey(w http.ResponseWriter, r *http.Request, ev model.UpgradeVerification, id string) {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		Name         *string `json:"name"`
		NameRevision *int64  `json:"nameRevision"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	var trailing any
	if d.Decode(&req) != nil || req.Name == nil || req.NameRevision == nil || d.Decode(&trailing) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key, err := u.keys.Rename(r.Context(), id, *req.Name, *req.NameRevision)
	if errors.Is(err, freshkeys.ErrNotFound) && ev.TargetMode == model.UpgradeTargetManaged {
		if _, _, _, regErr := u.resolveManagedAuthority(r.Context()); regErr == nil {
			key, err = u.keys.Rename(r.Context(), id, *req.Name, *req.NameRevision)
		}
	}
	switch {
	case errors.Is(err, freshkeys.ErrRequest):
		w.WriteHeader(http.StatusBadRequest)
		return
	case errors.Is(err, freshkeys.ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
		return
	case errors.Is(err, freshkeys.ErrConflict):
		w.WriteHeader(http.StatusConflict)
		return
	case err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeFreshJSON(w, key)
}

func (u *upgradeKeyWorkspace) observeKeyForRead(
	w http.ResponseWriter,
	ctx context.Context,
	authority *freshkeyapply.Service,
	isExternal bool,
	key freshkeys.Key,
	generation uint64,
) (freshkeys.Key, bool) {
	if isExternal {
		preGen, preErr := u.precheckExternal(ctx)
		switch {
		case errors.Is(preErr, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return key, false
		case errors.Is(preErr, errExternalRejectionUnavailable), errors.Is(preErr, cpa.ErrExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
			return key, false
		case errors.Is(preErr, cpa.ErrManagementClientForbidden):
			writeExternalManagementForbidden(w)
			return key, false
		case errors.Is(preErr, cpa.ErrManagementRejected):
			writeExternalManagementRejected(w)
			return key, false
		case errors.Is(preErr, cpa.ErrExternalTargetMoved):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{freshconfig.ReasonTargetMoved})
			return key, false
		case preErr != nil && !errors.Is(preErr, freshkeyapply.ErrExternalUnavailable):
			status, code := keyOperationCode(preErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{code})
			return key, false
		case preErr == nil:
			generation = preGen
			ctx = u.withExternalGate(ctx)
		default:
			generation = 0
		}
	}
	observed, err := authority.ObserveKey(ctx, key, generation)
	if err != nil {
		switch {
		case errors.Is(err, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
		case errors.Is(err, errExternalRejectionUnavailable), errors.Is(err, cpa.ErrExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
		case errors.Is(err, cpa.ErrManagementClientForbidden):
			writeExternalManagementForbidden(w)
		case errors.Is(err, cpa.ErrManagementRejected):
			writeExternalManagementRejected(w)
		case errors.Is(err, cpa.ErrExternalTargetMoved):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{freshconfig.ReasonTargetMoved})
		default:
			status, code := keyOperationCode(err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{code})
		}
		return key, false
	}
	return observed, true
}

func (u *upgradeKeyWorkspace) serveCreate(
	w http.ResponseWriter,
	r *http.Request,
	authority *freshkeyapply.Service,
	isExternal bool,
	generation uint64,
	runtimeID string,
) {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		RequestID string `json:"requestId"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || !freshkeys.ValidRequestID(req.RequestID) || d.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if isExternal {
		install, preErr := u.precheckExternalInstall(ctx)
		if preErr != nil {
			switch {
			case errors.Is(preErr, errExternalCredentialWaitCancelled):
				writeExternalCredentialWaitCancelled(w)
			case errors.Is(preErr, errExternalRejectionUnavailable):
				writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", nil, nil)
			case errors.Is(preErr, cpa.ErrExternalTargetMoved):
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonTargetMoved, "", nil, nil)
			case errors.Is(preErr, cpa.ErrManagementClientForbidden):
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "client_forbidden", nil, nil)
			case errors.Is(preErr, cpa.ErrManagementRejected):
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "", nil, nil)
			default:
				status, code := keyOperationCode(preErr)
				writeCreateKeyError(w, status, code, "", nil, nil)
			}
			return
		}
		generation = install.Checkpoint
		runtimeID = install.Identity
		ctx = u.withExternalGate(ctx)
	}
	createCtx := r.Context()
	if runtimeID != "" {
		createCtx = withUpgradeRuntimeIdentity(createCtx, runtimeID)
	}
	result, created, err := u.keys.Create(createCtx, req.RequestID)
	if err != nil {
		if errors.Is(err, freshkeys.ErrRequest) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if created && u.wakeProjection != nil {
		u.wakeProjection()
	}
	key := result.Key
	opRes, applyErr := authority.ApplyAdd(ctx, req.RequestID, string(key.ID), generation)
	if applyErr == nil && opRes.RuntimeState != "effective_observed" {
		applyErr = cpa.ErrApplyUnknown
	}
	if refreshed, detailErr := u.keys.Detail(r.Context(), string(key.ID)); detailErr == nil {
		key = refreshed
	}
	if applyErr != nil {
		if errors.Is(applyErr, errExternalCredentialWaitCancelled) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		applyCreateErrorKeyState(&key, opRes)
		if errors.Is(applyErr, cpa.ErrExternalRejectionUnavailable) {
			key.EffectCode = freshconfig.ReasonUnavailable
			var resPtr *freshkeyapply.OperationResult
			if opRes.Operation != nil {
				resPtr = &opRes
			}
			writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", &key, resPtr)
			return
		}
		status, code := keyOperationCode(applyErr)
		detail := ""
		if errors.Is(applyErr, cpa.ErrManagementClientForbidden) {
			detail = "client_forbidden"
		}
		key.EffectCode = code
		var resPtr *freshkeyapply.OperationResult
		if opRes.Operation != nil {
			resPtr = &opRes
		}
		writeCreateKeyError(w, status, code, detail, &key, resPtr)
		return
	}
	key.RuntimeState = "effective_observed"
	key.OperationID = ""
	key.EffectCode = ""
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	resp := clientKeyCreateResponse{
		Key:       key,
		Operation: opRes.Operation,
	}
	if created {
		resp.CreatedSecret = result.CreatedSecret
	}
	writeFreshJSON(w, resp)
}

func applyCreateErrorKeyState(key *freshkeys.Key, opRes freshkeyapply.OperationResult) {
	if key == nil {
		return
	}
	if opRes.Operation != nil && opRes.Operation.TargetID == string(key.ID) {
		if opRes.Operation.IsTerminal() {
			key.OperationID = ""
		} else {
			key.OperationID = opRes.Operation.ID
		}
		switch opRes.Operation.Outcome {
		case "pending":
			key.RuntimeState = "applying"
		case "unknown":
			key.RuntimeState = "effect_unknown"
		case "aborted":
			key.RuntimeState = "not_applied"
			key.OperationID = ""
		case "committed":
			key.OperationID = ""
			if opRes.RuntimeState != "" {
				key.RuntimeState = opRes.RuntimeState
			} else {
				key.RuntimeState = "effect_unknown"
			}
		}
		return
	}
	if key.OperationID == "" {
		if opRes.RuntimeState != "" {
			key.RuntimeState = opRes.RuntimeState
		} else {
			key.RuntimeState = "not_applied"
		}
	}
}

func (u *upgradeKeyWorkspace) servePreflight(
	w http.ResponseWriter,
	r *http.Request,
	authority *freshkeyapply.Service,
	isExternal bool,
	generation uint64,
	target, keeper, mode string,
) {
	ctx := r.Context()
	if isExternal {
		preGen, preErr := u.precheckExternal(ctx)
		switch {
		case errors.Is(preErr, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return
		case errors.Is(preErr, errExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
			return
		case errors.Is(preErr, cpa.ErrExternalTargetMoved):
			keyOperationPreflightReply(w, false, freshconfig.ReasonTargetMoved)
			return
		case errors.Is(preErr, cpa.ErrManagementClientForbidden), errors.Is(preErr, cpa.ErrManagementRejected):
			keyOperationPreflightReply(w, false, freshconfig.ReasonManagementRejected)
			return
		case preErr != nil:
			status, code := keyOperationCode(preErr)
			if status == http.StatusServiceUnavailable {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			keyOperationPreflightReply(w, false, code)
			return
		case preErr == nil:
			generation = preGen
		}
		ctx = u.withExternalGate(ctx)
	}
	err := authority.Preflight(ctx, target, keeper, mode, generation)
	if errors.Is(err, errExternalCredentialWaitCancelled) {
		writeExternalCredentialWaitCancelled(w)
		return
	}
	if errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
		writeExternalAuthorityUnavailable(w)
		return
	}
	reason := ""
	if err != nil {
		_, reason = keyOperationCode(err)
	}
	keyOperationPreflightReply(w, err == nil, reason)
}

func (u *upgradeKeyWorkspace) servePrepare(
	w http.ResponseWriter,
	r *http.Request,
	authority *freshkeyapply.Service,
	isExternal bool,
	generation uint64,
	target string,
) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		KeeperID  string `json:"keeperId"`
		Mode      string `json:"mode"`
	}
	if !decodeKeyOperation(w, r, &body) {
		return
	}
	ctx := r.Context()
	if isExternal {
		preGen, preErr := u.precheckExternal(ctx)
		switch {
		case errors.Is(preErr, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return
		case errors.Is(preErr, errExternalRejectionUnavailable):
			writeExternalAuthorityUnavailable(w)
			return
		case errors.Is(preErr, cpa.ErrExternalTargetMoved):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{freshconfig.ReasonTargetMoved})
			return
		case errors.Is(preErr, cpa.ErrManagementClientForbidden):
			writeExternalManagementForbidden(w)
			return
		case errors.Is(preErr, cpa.ErrManagementRejected):
			writeExternalManagementRejected(w)
			return
		case preErr != nil:
			keyOperationReply(w, freshkeyapply.OperationResult{}, preErr)
			return
		case preErr == nil:
			generation = preGen
		}
		ctx = u.withExternalGate(ctx)
	}
	res, err := authority.Prepare(ctx, body.RequestID, target, body.KeeperID, body.Mode, generation)
	if res.Operation != nil && res.Operation.Outcome == "committed" && res.Operation.Mode != "add" && u.wakeProjection != nil {
		u.wakeProjection()
	}
	keyOperationReply(w, res, err)
}

func (u *upgradeKeyWorkspace) serveKeyOperation(
	w http.ResponseWriter,
	r *http.Request,
	authority *freshkeyapply.Service,
	isExternal bool,
	generation uint64,
	resource string,
) {
	id := strings.TrimPrefix(resource, "api-keys/operations/")
	ctx := r.Context()
	if r.Method == http.MethodGet && !strings.Contains(id, "/") {
		if isExternal {
			preGen, preErr := u.precheckExternal(ctx)
			switch {
			case errors.Is(preErr, errExternalCredentialWaitCancelled):
				writeExternalCredentialWaitCancelled(w)
				return
			case errors.Is(preErr, errExternalRejectionUnavailable):
				writeExternalAuthorityUnavailable(w)
				return
			case errors.Is(preErr, cpa.ErrExternalTargetMoved):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				writeFreshJSON(w, struct {
					Code string `json:"code"`
				}{freshconfig.ReasonTargetMoved})
				return
			case errors.Is(preErr, cpa.ErrManagementClientForbidden):
				writeExternalManagementForbidden(w)
				return
			case errors.Is(preErr, cpa.ErrManagementRejected):
				writeExternalManagementRejected(w)
				return
			case preErr != nil:
				keyOperationReply(w, freshkeyapply.OperationResult{}, preErr)
				return
			case preErr == nil:
				generation = preGen
			}
			ctx = u.withExternalGate(ctx)
		}
		res, err := authority.CheckOperation(ctx, id, generation)
		if res.Operation != nil && res.Operation.Outcome == "committed" && res.Operation.Mode != "add" && u.wakeProjection != nil {
			u.wakeProjection()
		}
		keyOperationReply(w, res, err)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(id, "/continue") && strings.Count(id, "/") == 1 {
		var body map[string]any
		if !decodeKeyOperation(w, r, &body) {
			return
		}
		if body == nil || len(body) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if isExternal {
			preGen, preErr := u.precheckExternal(ctx)
			switch {
			case errors.Is(preErr, errExternalCredentialWaitCancelled):
				writeExternalCredentialWaitCancelled(w)
				return
			case errors.Is(preErr, errExternalRejectionUnavailable):
				writeExternalAuthorityUnavailable(w)
				return
			case errors.Is(preErr, cpa.ErrExternalTargetMoved):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				writeFreshJSON(w, struct {
					Code string `json:"code"`
				}{freshconfig.ReasonTargetMoved})
				return
			case errors.Is(preErr, cpa.ErrManagementClientForbidden):
				writeExternalManagementForbidden(w)
				return
			case errors.Is(preErr, cpa.ErrManagementRejected):
				writeExternalManagementRejected(w)
				return
			case preErr != nil:
				keyOperationReply(w, freshkeyapply.OperationResult{}, preErr)
				return
			case preErr == nil:
				generation = preGen
			}
			ctx = u.withExternalGate(ctx)
		}
		res, err := authority.ContinueOperation(ctx, strings.TrimSuffix(id, "/continue"), generation)
		if res.Operation != nil && res.Operation.Outcome == "committed" && res.Operation.Mode != "add" && u.wakeProjection != nil {
			u.wakeProjection()
		}
		keyOperationReply(w, res, err)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
