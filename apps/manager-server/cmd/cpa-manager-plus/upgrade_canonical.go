package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	settingrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// testUpgradeFullWebDist overrides the embedded Full Web bundle in source-only
// Go unit tests where web/fullweb has not been built yet. Nil in production.
var testUpgradeFullWebDist fs.FS

type upgradeCanonicalServer struct {
	mu                sync.RWMutex
	op                model.UpgradeOperation
	targetMode        string
	sourceFingerprint string

	db               *store.Store
	appCtx           *app.Context
	inner            http.Handler
	driver           freshrouting.Driver
	gatewayAuthority func(context.Context) bool

	hostCtx            context.Context
	protector          *security.Protector
	workspace          *upgradeKeyWorkspace
	runtime            runtimeservice.RuntimeClient
	artifact           *runtimeservice.ArtifactClient
	runtimeExternal    *runtimeservice.ExternalClient
	runtimeJournal     *freshRuntimeJournal
	updates            *updatecheck.Service
	configService      *upgradeConfigService
	runtimeLifecycleMu runtimeLifecycleMutex
	pluginCapability   *pluginCapabilityProbe
}

func failClosedUpgradeHandler(code string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			Code   string `json:"code"`
		}{
			Status: "unavailable",
			Code:   code,
		})
	})
}

func validateCompletedUpgradeRoutingRow(op model.UpgradeOperation, targetMode string) error {
	if op.Stage != model.UpgradeStageComplete {
		return errors.New("upgrade operation is not complete")
	}
	if !op.RoutingCommitted || strings.TrimSpace(op.ControlBasePath) == "" {
		return errors.New("completed upgrade operation has no committed access routing")
	}
	if err := routing.ValidateAccessPrefixPair(op.ControlBasePath, "", false, true); err != nil {
		return err
	}
	switch targetMode {
	case model.UpgradeTargetExternal:
		if op.InferenceBasePath != "" || op.InferenceCommitted || op.SharedEntry {
			return errors.New("completed External upgrade carries forbidden managed inference routing")
		}
		return nil
	case model.UpgradeTargetManaged:
		if op.InferenceCommitted {
			if strings.TrimSpace(op.InferenceBasePath) == "" {
				return errors.New("completed Managed upgrade claims committed inference without a prefix")
			}
			return routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false)
		}
		if op.InferenceBasePath != "" {
			return routing.ValidateAccessPrefixPair(op.ControlBasePath, op.InferenceBasePath, op.SharedEntry, false)
		}
		if op.SharedEntry {
			return errors.New("completed Managed upgrade carries sharedEntry without an inference prefix")
		}
		return nil
	default:
		return errors.New("completed upgrade operation has unsupported target mode")
	}
}

func attachCompletedUpgradeRouting(
	ctx context.Context,
	cfg config.Config,
	db *store.Store,
	serverApp *httpapi.Server,
	gatewayAuthority func(context.Context) bool,
	deps ...upgradeCanonicalDependencies,
) error {
	if db == nil || serverApp == nil {
		return nil
	}
	failClosed := func(code string, err error) error {
		serverApp.WrapHandler(func(_ http.Handler) http.Handler {
			return failClosedUpgradeHandler(code)
		})
		return err
	}
	repo := upgradeoperation.New(db.SQLDB())
	op, found, err := repo.Get(ctx)
	if err != nil {
		return failClosed("upgrade_authority_unavailable", err)
	}
	if !found {
		return nil
	}
	if op.Stage != model.UpgradeStageComplete {
		return failClosed("upgrade_not_complete", errors.New("completed upgrade routing requires a completed upgrade operation"))
	}
	verifiedOp, evidence, verified, verifyErr := loadVerifiedCompletedUpgradeOperation(ctx, repo)
	if verifyErr != nil || !verified {
		if verifyErr == nil {
			verifyErr = errors.New("completed upgrade operation failed provenance verification")
		}
		return failClosed("upgrade_authority_unavailable", verifyErr)
	}
	op = verifiedOp
	if !op.RoutingCommitted || strings.TrimSpace(op.ControlBasePath) == "" {
		return failClosed(
			string(upgradeservice.UpgradeErrorVerifyAccessPrefixRequired),
			errors.New("completed upgrade operation has no committed access routing; run 'cpa-manager-plus upgrade verify --control-base-path <path> [--inference-base-path <path>]' to commit missing prefixes"),
		)
	}
	if err := validateCompletedUpgradeRoutingRow(op, evidence.TargetMode); err != nil {
		return failClosed("upgrade_authority_unavailable", err)
	}

	var driver freshrouting.Driver
	controlURL := strings.TrimSpace(os.Getenv("CPAMP_INGRESS_CONTROL_URL"))
	publicOrigin := strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN"))
	if controlURL != "" {
		d, driverErr := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{
			IngressControlURL: controlURL,
			PublicOrigin:      publicOrigin,
		})
		if driverErr != nil {
			return failClosed("upgrade_authority_unavailable", driverErr)
		}
		driver = d
	}

	u := &upgradeCanonicalServer{
		op:                op,
		targetMode:        evidence.TargetMode,
		sourceFingerprint: strings.TrimSpace(evidence.SourceFingerprint),
		db:                db,
		appCtx:            serverApp.AppContext(),
		inner:             serverApp.Handler(),
		driver:            driver,
		gatewayAuthority:  gatewayAuthority,
	}
	var dep upgradeCanonicalDependencies
	if len(deps) > 0 {
		dep = deps[0]
	}
	if err := u.initCanonicalResources(ctx, cfg, dep); err != nil {
		return failClosed("upgrade_authority_unavailable", err)
	}
	if u.gatewayAuthority == nil && u.workspace != nil {
		u.gatewayAuthority = u.workspace.gatewayAdmitted
	}
	if u.appCtx != nil && u.appCtx.UpgradeAPIKeys == nil && u.workspace != nil {
		serverApp.SetUpgradeAPIKeys(u.workspace.serveHTTP)
	}
	serverApp.WrapHandler(func(base http.Handler) http.Handler {
		u.inner = base
		return requestbody.Middleware(requestbody.DefaultReadIdleTimeout, httpapi.ProtectBrandResources(withSupportPluginHeader(u, u.supportPluginCapable)))
	})
	if u.driver != nil {
		u.publishToIngress(ctx)
		go u.reconcileIngressLoop(ctx)
	}
	return nil
}

func (u *upgradeCanonicalServer) syncOp(candidate model.UpgradeOperation) model.UpgradeOperation {
	u.mu.Lock()
	defer u.mu.Unlock()
	if candidate.Revision >= u.op.Revision {
		u.op = candidate
	}
	return u.op
}

func (u *upgradeCanonicalServer) loadCurrentOp(ctx context.Context) (model.UpgradeOperation, error) {
	if u == nil || u.db == nil || u.db.SQLDB() == nil {
		return model.UpgradeOperation{}, errors.New("upgrade database unavailable")
	}
	loaded, ev, ok, err := loadVerifiedCompletedUpgradeOperation(ctx, upgradeoperation.New(u.db.SQLDB()))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !ok {
		return model.UpgradeOperation{}, errors.New("upgrade operation incomplete or invalid")
	}
	if ev.TargetMode != u.targetMode || strings.TrimSpace(ev.SourceFingerprint) != u.sourceFingerprint {
		return model.UpgradeOperation{}, errors.New("upgrade verification evidence drifted")
	}
	if err := validateCompletedUpgradeRoutingRow(loaded, u.targetMode); err != nil {
		return model.UpgradeOperation{}, err
	}
	return u.syncOp(loaded), nil
}

func writeUpgradeAuthorityUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}{
		Status: "unavailable",
		Code:   "upgrade_authority_unavailable",
	})
}
func (u *upgradeCanonicalServer) authorityID() string {
	if u.sourceFingerprint != "" {
		return "upgrade:" + u.sourceFingerprint
	}
	return "upgrade:v2"
}

func (u *upgradeCanonicalServer) routingRevision(op model.UpgradeOperation) uint64 {
	if u.targetMode == model.UpgradeTargetManaged && op.InferenceBasePath != "" {
		return 2
	}
	return 1
}

func (u *upgradeCanonicalServer) operationRevision(op model.UpgradeOperation) uint64 {
	if op.Revision >= 0 {
		return uint64(op.Revision) + 1
	}
	return 1
}

func (u *upgradeCanonicalServer) missingInferenceCompletionRequired(op model.UpgradeOperation) bool {
	return u.targetMode == model.UpgradeTargetManaged && (!op.InferenceCommitted || op.InferenceBasePath == "")
}

func (u *upgradeCanonicalServer) stateFor(op model.UpgradeOperation) freshrouting.State {
	authID := u.authorityID()
	infTarget := ""
	infCommitted := false
	shared := false
	if u.targetMode == model.UpgradeTargetManaged && op.InferenceBasePath != "" {
		infTarget = op.InferenceBasePath
		infCommitted = op.RoutingCommitted && op.InferenceCommitted
		shared = op.SharedEntry
	}
	return freshrouting.State{
		InstallationID:     authID,
		Generation:         authID,
		OperationID:        bootstrap.OperationID(authID),
		Revision:           u.routingRevision(op),
		Target:             op.ControlBasePath,
		InferenceTarget:    infTarget,
		SharedEntry:        shared,
		Committed:          true,
		InferenceCommitted: infCommitted,
	}
}

func (u *upgradeCanonicalServer) snapshotFor(op model.UpgradeOperation) routing.RoutingSnapshot {
	missingInf := u.missingInferenceCompletionRequired(op)
	snap := routing.RoutingSnapshot{
		State:                              routing.SnapshotVerifiedEffective,
		DesiredControlBasePath:             op.ControlBasePath,
		VerifiedEffectiveControlBasePath:   op.ControlBasePath,
		MissingInferenceCompletionRequired: missingInf,
		Revision:                           u.routingRevision(op),
	}
	if u.targetMode == model.UpgradeTargetManaged {
		switch {
		case op.InferenceCommitted && op.InferenceBasePath != "":
			snap.DesiredInferenceBasePath = op.InferenceBasePath
			snap.VerifiedEffectiveInferenceBasePath = op.InferenceBasePath
			snap.InferenceState = routing.SnapshotVerifiedEffective
			snap.SharedEntry = op.SharedEntry
		case op.InferenceBasePath != "":
			snap.DesiredInferenceBasePath = op.InferenceBasePath
			snap.InferenceState = routing.SnapshotDesiredOnly
			snap.SharedEntry = op.SharedEntry
		default:
			snap.InferenceState = routing.SnapshotUnavailable
		}
	}
	if snap.Validate() != nil {
		return routing.RoutingSnapshot{State: routing.SnapshotUnavailable}
	}
	return snap
}

func (u *upgradeCanonicalServer) gatewayAdmitted(ctx context.Context) bool {
	op, err := u.loadCurrentOp(ctx)
	if err != nil {
		return false
	}
	if u.targetMode != model.UpgradeTargetManaged || !op.RoutingCommitted || !op.InferenceCommitted || op.InferenceBasePath == "" {
		return false
	}
	if u.appCtx != nil && u.appCtx.ManagerConfigService != nil {
		if _, ok, err := u.appCtx.ManagerConfigService.ResolveSetup(ctx); err != nil || !ok {
			return false
		}
	}
	return u.gatewayAuthority != nil && u.gatewayAuthority(ctx)
}

func (u *upgradeCanonicalServer) fullWebHandler(snap routing.RoutingSnapshot, api http.Handler) http.Handler {
	if testUpgradeFullWebDist != nil {
		return fullweb.Handler{Snapshot: snap, Dist: testUpgradeFullWebDist, API: api}
	}
	return httpapi.FullWebHandler(snap, api)
}

func (u *upgradeCanonicalServer) publishToIngress(ctx context.Context) {
	if u.driver == nil {
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	op, err := u.loadCurrentOp(pubCtx)
	if err != nil {
		log.Printf("skip publishing upgraded routing state to ingress while store is unavailable: %v", err)
		return
	}
	if err := u.driver.Apply(pubCtx, u.stateFor(op)); err != nil {
		log.Printf("publish upgraded routing state to ingress: %v", err)
	}
}

func (u *upgradeCanonicalServer) reconcileIngressLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if u.driver == nil {
				return
			}
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			actual, err := u.driver.Inspect(checkCtx)
			op, opErr := u.loadCurrentOp(checkCtx)
			cancel()
			if err == nil && opErr == nil && actual != u.stateFor(op) {
				u.publishToIngress(ctx)
			}
		}
	}
}

func (u *upgradeCanonicalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/health" && r.URL.EscapedPath() == "/health" {
		if _, err := u.loadCurrentOp(r.Context()); err != nil {
			writeUpgradeAuthorityUnavailable(w)
			return
		}
		u.inner.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == freshrouting.AuthorityPath && r.URL.EscapedPath() == freshrouting.AuthorityPath && r.URL.RawQuery == "" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		op, err := u.loadCurrentOp(r.Context())
		if err != nil {
			writeUpgradeAuthorityUnavailable(w)
			return
		}
		state := u.stateFor(op)
		admitted := u.gatewayAdmitted(r.Context())
		authReply := freshAuthorityReply{
			State: state,
			GatewayAdmission: freshGatewayAdmission{
				InstallationID: state.InstallationID,
				Generation:     state.Generation,
				Admitted:       admitted,
			},
		}
		writeFreshJSON(w, authReply)
		return
	}
	if u.inner != nil && pluginResourceRequest(r) {
		u.servePluginResource(w, r)
		return
	}
	op, err := u.loadCurrentOp(r.Context())
	if err != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	snap := u.snapshotFor(op)
	prefix, ok := snap.EffectiveControlBasePath()
	if !ok {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
		handler := middleware.RejectCrossSiteAdmin(
			u.fullWebHandler(snap, http.HandlerFunc(u.serveCanonicalAPI)),
			proxysvc.IsCanonicalPluginResourceRequest,
		)
		handler.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (u *upgradeCanonicalServer) verifyAdminBearer(ctx context.Context, r *http.Request) (bool, error) {
	vals := r.Header.Values("Authorization")
	if len(vals) != 1 || u == nil || u.db == nil || u.db.SQLDB() == nil {
		return false, nil
	}
	bearer := security.ExtractBearerToken(vals[0])
	if bearer == "" {
		return false, nil
	}
	cred, ok, err := settingrepo.New(u.db.SQLDB()).LoadAdminCredential(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return security.VerifyAdminKey(cred, bearer), nil
}

func (u *upgradeCanonicalServer) serveCanonicalAPI(w http.ResponseWriter, r *http.Request) {
	op, err := u.loadCurrentOp(r.Context())
	if err != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	apiPrefix := op.ControlBasePath + "/api/v2/"
	if !strings.HasPrefix(r.URL.Path, apiPrefix) {
		http.NotFound(w, r)
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if resource == "bootstrap/identity" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeFreshJSON(w, freshrouting.PublicIdentityForState(u.stateFor(op)))
		return
	}
	authorized, authErr := u.verifyAdminBearer(r.Context(), r)
	if authErr != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if !authorized {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case resource == "operation" || resource == "installation/operation":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeFreshJSON(w, u.operationDocument(r.Context(), op))
	case resource == "installation/access/completion":
		u.serveMissingInferenceCompletion(w, r)
	case resource == "api-keys/access-info" || resource == "access-info/default":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		u.serveDefaultAccessInfo(w, r, op)
	case resource == "api-keys" || strings.HasPrefix(resource, "api-keys/"):
		if u.appCtx != nil && u.appCtx.UpgradeAPIKeys != nil {
			u.appCtx.UpgradeAPIKeys(w, r, resource)
			return
		}
		if u.workspace != nil {
			u.workspace.serveHTTP(w, r, resource)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	case resource == "gateway/config" || strings.HasPrefix(resource, "gateway/config/"):
		u.serveGatewayConfig(w, r, resource)
	case resource == "plugins/runtime" || strings.HasPrefix(resource, "plugins/runtime/"):
		u.servePluginRuntime(w, r, resource)
	case resource == "settings/update-channel":
		u.serveUpdateChannel(w, r)
	case resource == "runtime/status":
		u.serveRuntimeStatus(w, r)
	case resource == "runtime/artifact" || strings.HasPrefix(resource, "runtime/artifact/"):
		u.serveRuntimeArtifact(w, r, resource)
	case resource == "runtime/operations" || strings.HasPrefix(resource, "runtime/operations/"):
		u.serveRuntimeOperations(w, r, resource)
	case resource == "external/usage" || strings.HasPrefix(resource, "external/usage/"):
		u.serveExternalUsage(w, r, resource)
	case resource == "external/rebind" || strings.HasPrefix(resource, "external/rebind/"):
		u.serveExternalRebind(w, r, resource)
	case strings.HasPrefix(resource, "cpamp/"):
		u.serveCPAMPBridge(w, r, op)
	default:
		http.NotFound(w, r)
	}
}

func (u *upgradeCanonicalServer) operationDocument(ctx context.Context, op model.UpgradeOperation) freshrouting.OperationDocument {
	state := u.stateFor(op)
	strategy := bootstrap.NewManaged
	gatewayPending := !u.gatewayAdmitted(ctx)
	if u.targetMode == model.UpgradeTargetExternal {
		strategy = bootstrap.ConnectExternal
		gatewayPending = false
	}
	_, sugInf := upgradeservice.DeterministicSuggestedPrefixes(op)
	missingInf := u.missingInferenceCompletionRequired(op)
	sugOut := ""
	if missingInf {
		sugOut = sugInf
	}
	return freshrouting.OperationDocument{
		Version:          bootstrap.FreshContractVersion,
		Identity:         bootstrap.FreshIdentity{InstallationID: state.InstallationID, Generation: state.Generation, OperationID: state.OperationID},
		Revision:         u.operationRevision(op),
		AdminInitialized: true,
		ProductComplete:  true,
		GatewayPending:   gatewayPending,
		Routing: freshrouting.RoutingDocument{
			OperationID:                        state.OperationID,
			Revision:                           state.Revision,
			Target:                             op.ControlBasePath,
			InferenceTarget:                    state.InferenceTarget,
			SharedEntry:                        state.SharedEntry,
			InferenceCommitted:                 state.InferenceCommitted,
			MissingInferenceCompletionRequired: missingInf,
			SuggestedInferenceBasePath:         sugOut,
			Stage:                              bootstrap.PrefixCommitted,
			Committed:                          true,
		},
		Strategy: bootstrap.FreshStrategyCheckpoint{
			Strategy: strategy,
			Stage:    "committed",
		},
	}
}

func (u *upgradeCanonicalServer) serveDefaultAccessInfo(w http.ResponseWriter, r *http.Request, op model.UpgradeOperation) {
	mode := bootstrap.MigrateLegacy
	externalURL := ""
	infPath := ""
	ready := u.gatewayAdmitted(r.Context())
	if u.targetMode == model.UpgradeTargetManaged && op.InferenceCommitted {
		infPath = op.InferenceBasePath
	}
	if u.targetMode == model.UpgradeTargetExternal {
		mode = bootstrap.ConnectExternal
		repo := upgradeoperation.New(u.db.SQLDB())
		binding, found, err := repo.ExternalBinding(r.Context())
		if err == nil && found {
			externalURL = upgradeservice.FencedExternalBaseURLForStore(r.Context(), u.db.SQLDB(), u.protector, op, binding, found)
			if externalURL != "" && u.workspace != nil && u.workspace.rejection != nil {
				credDigest := envelopeDigest(strings.TrimSpace(binding.ManagementKey))
				if refused, refErr := u.workspace.rejection.check(r.Context(), binding.Identity, credDigest); refErr != nil || refused {
					externalURL = ""
				}
			}
		}
		ready = externalURL != ""
	}
	info := makeKeyAccessInfo(mode, op.ControlBasePath, infPath, ready, externalURL)
	writeKeyAccessInfo(w, info)
}
func (u *upgradeCanonicalServer) completionMode() string {
	if u.targetMode == model.UpgradeTargetExternal {
		return "connect_external"
	}
	return "migrate_legacy"
}

func (u *upgradeCanonicalServer) completionStatusFor(op model.UpgradeOperation) freshrouting.AccessCompletionStatus {
	_, sugInf := upgradeservice.DeterministicSuggestedPrefixes(op)
	required := u.missingInferenceCompletionRequired(op)
	infPath := ""
	shared := false
	infCommitted := false
	if u.targetMode == model.UpgradeTargetManaged {
		infPath = op.InferenceBasePath
		shared = op.SharedEntry
		infCommitted = op.InferenceCommitted && op.InferenceBasePath != ""
	}
	resp := freshrouting.AccessCompletionStatus{
		Required:           required,
		Mode:               u.completionMode(),
		ControlBasePath:    op.ControlBasePath,
		InferenceBasePath:  infPath,
		SharedEntry:        shared,
		InferenceCommitted: infCommitted,
	}
	if required {
		resp.SuggestedInferenceBasePath = sugInf
	}
	return resp
}

func rejectUpgradeCrossSiteMutation(r *http.Request) bool {
	if r == nil {
		return true
	}
	if sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); sfs == "cross-site" || sfs == "same-site" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return true
	}
	return !strings.EqualFold(u.Host, strings.TrimSpace(r.Host))
}

func (u *upgradeCanonicalServer) writeDriverCompletionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, freshrouting.ErrConflict):
		writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeRoutingConflict), "routing conflict")
	case errors.Is(err, freshrouting.ErrUnverified):
		writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_verification_failed", "public inference route verification failed")
	default:
		writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "routing verification unavailable")
	}
}

func (u *upgradeCanonicalServer) tryFinishConcurrentCommit(
	w http.ResponseWriter,
	r *http.Request,
	repo *upgradeoperation.Repository,
	inferenceBasePath string,
	sharedEntry bool,
) bool {
	latest, found, err := repo.Get(r.Context())
	if err != nil || !found || !latest.InferenceCommitted {
		return false
	}
	latest = u.syncOp(latest)
	if latest.InferenceBasePath != inferenceBasePath || latest.SharedEntry != sharedEntry {
		writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeInferencePrefixAlreadyCommitted), "inference prefix is already committed")
		return true
	}
	if err := u.driver.Apply(r.Context(), u.stateFor(latest)); err != nil {
		u.writeDriverCompletionError(w, err)
		return true
	}
	writeFreshJSON(w, u.completionStatusFor(latest))
	return true
}

func (u *upgradeCanonicalServer) serveMissingInferenceCompletion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	switch r.Method {
	case http.MethodGet:
		op, err := u.loadCurrentOp(r.Context())
		if err != nil {
			writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "routing authority unavailable")
			return
		}
		writeFreshJSON(w, u.completionStatusFor(op))
	case http.MethodPost:
		if rejectUpgradeCrossSiteMutation(r) {
			writeUpgradeCompletionError(w, http.StatusForbidden, "origin_rejected", "cross-site access completion request rejected")
			return
		}
		var req struct {
			InferenceBasePath string `json:"inferenceBasePath"`
			SharedEntry       bool   `json:"sharedEntry,omitempty"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
			writeUpgradeCompletionError(w, http.StatusBadRequest, string(bootstrap.CodeInvalidAccessPrefix), "invalid access completion request body")
			return
		}
		if u.targetMode == model.UpgradeTargetExternal {
			writeUpgradeCompletionError(w, http.StatusBadRequest, string(bootstrap.CodeExternalInferencePrefixForbidden), "external installations do not configure a managed inference prefix")
			return
		}
		cur, err := u.loadCurrentOp(r.Context())
		if err != nil {
			writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "routing authority unavailable")
			return
		}
		if cur.InferenceCommitted && (cur.InferenceBasePath != req.InferenceBasePath || cur.SharedEntry != req.SharedEntry) {
			writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeInferencePrefixAlreadyCommitted), "inference prefix is already committed")
			return
		}
		if !cur.InferenceCommitted && cur.InferenceBasePath != "" && (cur.InferenceBasePath != req.InferenceBasePath || cur.SharedEntry != req.SharedEntry) {
			writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeInferencePrefixAlreadyCommitted), "pending inference prefix intent already exists for a different target")
			return
		}
		if err := routing.ValidateAccessPrefixPair(cur.ControlBasePath, req.InferenceBasePath, req.SharedEntry, false); err != nil {
			code, _ := bootstrap.AccessPrefixCodeOf(err)
			status := http.StatusBadRequest
			if code == bootstrap.CodeReservedPrefixCollision || code == bootstrap.CodePrefixOverlapForbidden || code == bootstrap.CodeSharedPrefixConfirmationRequired {
				status = http.StatusConflict
			}
			writeUpgradeCompletionError(w, status, string(code), err.Error())
			return
		}
		if u.driver == nil {
			writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "routing verification unavailable")
			return
		}
		if cur.InferenceCommitted {
			if err := u.driver.Apply(r.Context(), u.stateFor(cur)); err != nil {
				u.writeDriverCompletionError(w, err)
				return
			}
			writeFreshJSON(w, u.completionStatusFor(cur))
			return
		}
		if cur.InferenceBasePath == "" {
			actual, err := u.driver.Inspect(r.Context())
			if err != nil {
				u.writeDriverCompletionError(w, err)
				return
			}
			expectedAuth := u.stateFor(cur)
			if actual.OperationID != "" && (actual.OperationID != expectedAuth.OperationID ||
				actual.InstallationID != expectedAuth.InstallationID ||
				actual.Generation != expectedAuth.Generation ||
				actual.Target != cur.ControlBasePath ||
				(actual.InferenceTarget != "" && (actual.InferenceTarget != req.InferenceBasePath || actual.SharedEntry != req.SharedEntry))) {
				writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeRoutingConflict), "foreign or conflicting routing state on ingress")
				return
			}
		}
		repo := upgradeoperation.New(u.db.SQLDB())
		staged, err := repo.StageMissingInferenceOnCompletedUpgrade(r.Context(), req.InferenceBasePath, req.SharedEntry)
		if err != nil {
			switch {
			case errors.Is(err, upgradeoperation.ErrRoutingAlreadyCommitted):
				writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeInferencePrefixAlreadyCommitted), "inference prefix is already committed")
			case errors.Is(err, upgradeoperation.ErrConflict), errors.Is(err, upgradeoperation.ErrInvalidRow):
				writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeRoutingConflict), "routing state changed concurrently")
			default:
				writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "durable routing persistence unavailable")
			}
			return
		}
		staged = u.syncOp(staged)
		if staged.InferenceCommitted {
			if err := u.driver.Apply(r.Context(), u.stateFor(staged)); err != nil {
				u.writeDriverCompletionError(w, err)
				return
			}
			writeFreshJSON(w, u.completionStatusFor(staged))
			return
		}
		probeState := u.stateFor(staged)
		if err := u.driver.Apply(r.Context(), probeState); err != nil {
			if u.tryFinishConcurrentCommit(w, r, repo, req.InferenceBasePath, req.SharedEntry) {
				return
			}
			u.writeDriverCompletionError(w, err)
			return
		}
		if err := u.driver.Verify(r.Context(), probeState); err != nil {
			if u.tryFinishConcurrentCommit(w, r, repo, req.InferenceBasePath, req.SharedEntry) {
				return
			}
			u.writeDriverCompletionError(w, err)
			return
		}
		committed, err := repo.CommitMissingInferenceOnCompletedUpgrade(r.Context(), staged.Revision, req.InferenceBasePath, req.SharedEntry)
		if err != nil {
			switch {
			case errors.Is(err, upgradeoperation.ErrRoutingAlreadyCommitted):
				writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeInferencePrefixAlreadyCommitted), "inference prefix is already committed")
			case errors.Is(err, upgradeoperation.ErrConflict), errors.Is(err, upgradeoperation.ErrInvalidRow):
				writeUpgradeCompletionError(w, http.StatusConflict, string(bootstrap.CodeRoutingConflict), "routing state changed concurrently")
			default:
				writeUpgradeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "durable routing persistence unavailable")
			}
			return
		}
		committed = u.syncOp(committed)
		if err := u.driver.Apply(r.Context(), u.stateFor(committed)); err != nil {
			u.writeDriverCompletionError(w, err)
			return
		}
		writeFreshJSON(w, u.completionStatusFor(committed))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeUpgradeCompletionError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code      string `json:"code"`
		UpperCode string `json:"Code"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Code:      code,
		UpperCode: code,
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message},
	})
}

func (u *upgradeCanonicalServer) serveCPAMPBridge(w http.ResponseWriter, r *http.Request, op model.UpgradeOperation) {
	w.Header().Set("Cache-Control", "no-store")
	apiPrefix := op.ControlBasePath + "/api/v2/"
	resource := strings.TrimPrefix(r.URL.Path, apiPrefix)
	u.serveBridgedLegacyResource(w, r, resource)
}
