package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// initialize is a host-owned offline operation. It admits the actual store and
// returns a bounded fragment handoff to its caller, never a service log or a
// second long-running daemon. The normal service starts/resumes the same store.
func runFreshInitialize(ctx context.Context, stdout io.Writer) error {
	cfg, err := config.LoadForInspection()
	if err != nil {
		return err
	}
	if err := validateFreshHostAuthority(cfg); err != nil {
		return err
	}
	paths, err := freshPaths(cfg)
	if err != nil {
		return err
	}
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: cfg.AdminKey, DataKey: cfg.DataKey})
	if err != nil {
		return err
	}
	defer opened.Close()
	handoff, err := fresh.New(freshstore.New(opened.DB)).Issue(ctx)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, bootstrap.FixedBootstrapUIPath+handoff.Fragment+"\n")
	return err
}

func freshPaths(cfg config.Config) (sqliterepo.InspectionPaths, error) {
	configPath, err := config.InspectionConfigPath()
	return sqliterepo.InspectionPaths{Database: cfg.DBPath, Config: configPath, DataKey: cfg.DataKeyPath, Archives: cfg.UsageArchiveDir}, err
}

type freshApplication struct {
	store     freshstore.Store
	authority *fresh.Service
	routing   *freshrouting.Service
	strategy  *freshstrategy.Service
	protector *security.Protector
	fixed     http.Handler
}

func newFreshApplication(opened *fresh.Opened, cfg config.Config, driver freshrouting.Driver) *freshApplication {
	st := freshstore.New(opened.DB)
	a := &freshApplication{store: st, authority: fresh.New(st), routing: freshrouting.New(st, driver), protector: opened.Protector}
	var runtime runtimeservice.RuntimeClient
	if cfg.EmbeddedRuntimeConfigured() {
		runtime = runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile))
	}
	// Only the build-bound consumer record can grant production qualification.
	// Pending/missing evidence retains the existing typed strategy blockers.
	a.strategy = freshstrategy.NewProduction(freshstrategy.Options{Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: cfg.CPAUpstreamURL})
	a.fixed = freshbootstrap.Handler{Authority: a.authority, Routing: a.routing, Current: a.current,
		Shell: httpapi.BootstrapWebHandler(), Strategy: http.HandlerFunc(a.fixedStrategy)}
	return a
}

func (a *freshApplication) current(ctx context.Context) (bootstrap.FreshOperation, error) {
	r, err := a.store.Load(ctx)
	return r.Operation, err
}

func (a *freshApplication) validateCommittedExternalReplay(ctx context.Context, op bootstrap.FreshOperation, request freshstrategy.Request) error {
	if op.Strategy.Strategy != bootstrap.ConnectExternal || op.Strategy.Stage != "committed" || request.Strategy != bootstrap.ConnectExternal {
		return nil
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return err
	}
	if record.Operation.Identity != op.Identity || record.Operation.Revision != op.Revision {
		return &freshstrategy.Blocker{Code: "fresh_strategy_owner_conflict"}
	}
	if record.Connection == nil || a.protector == nil {
		return &freshstrategy.Blocker{Code: "management_secret_storage_unavailable"}
	}
	baseURL, err := cpa.NormalizeV8BaseURL(request.BaseURL)
	if err != nil {
		return &freshstrategy.Blocker{Code: cpa.V8InvalidConnection.Error()}
	}
	if record.Connection.CPABaseURL != baseURL {
		return &freshstrategy.Blocker{Code: "fresh_strategy_owner_conflict"}
	}
	persistedKey, err := a.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil {
		return &freshstrategy.Blocker{Code: "management_secret_storage_unavailable"}
	}
	if !security.EqualHMAC(persistedKey, request.ManagementKey) {
		return &freshstrategy.Blocker{Code: "fresh_strategy_owner_conflict"}
	}
	return nil
}

func writeFreshStrategyError(w http.ResponseWriter, err error) {
	var blocker *freshstrategy.Blocker
	if errors.As(err, &blocker) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeFreshJSON(w, blocker)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (a *freshApplication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r != nil && r.URL != nil && r.URL.Path == freshrouting.AuthorityPath && r.URL.EscapedPath() == r.URL.Path && r.URL.RawQuery == "" {
		a.routingAuthority(w, r)
		return
	}
	protect := routing.ProtectFixedBootstrap(http.HandlerFunc(a.canonical), a.fixed)
	httpapi.ProtectBrandResources(protect).ServeHTTP(w, r)
}

func (a *freshApplication) routingAuthority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	op, err := a.current(r.Context())
	if err != nil || op.Routing.Target == "" || op.Routing.OperationID == "" {
		http.NotFound(w, r)
		return
	}
	writeFreshJSON(w, freshrouting.StateFor(op))
}

func (a *freshApplication) canonical(w http.ResponseWriter, r *http.Request) {
	op, err := a.current(r.Context())
	if err != nil || op.Routing.Target == "" {
		http.NotFound(w, r)
		return
	}
	api := http.HandlerFunc(a.canonicalAPI)
	if op.Routing.Committed {
		snapshot := a.routing.Snapshot(r.Context())
		if !snapshot.PublicAPIActive() {
			http.NotFound(w, r)
			return
		}
		httpapi.FullWebHandler(snapshot, api).ServeHTTP(w, r)
		return
	}
	// The pending route is explicitly owned by the existing Fresh checkpoint;
	// it is a verification candidate and never a verified-effective snapshot.
	httpapi.InitialFullWebHandler(op.Routing.Target, api).ServeHTTP(w, r)
}

func (a *freshApplication) canonicalAPI(w http.ResponseWriter, r *http.Request) {
	record, err := a.store.Load(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	op := record.Operation
	base := op.Routing.Target + routing.PublicAPIV2Suffix + "/"
	resource := strings.TrimPrefix(r.URL.Path, base)
	if resource == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	// Non-secret identity is the bounded real-public-entry verification leaf.
	// It authenticates no browser and cannot mutate the pending installation.
	if resource == "bootstrap/identity" && r.Method == http.MethodGet {
		writeFreshJSON(w, freshrouting.StateFor(op))
		return
	}
	if !op.Routing.Committed || record.Admin == nil || !security.VerifyAdminKey(*record.Admin, security.ExtractBearerToken(r.Header.Get("Authorization"))) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case resource == "operation" && r.Method == http.MethodGet:
		writeFreshJSON(w, op)
	case resource == "strategy" && r.Method == http.MethodPost:
		a.executeStrategy(w, r, op)
	case resource == "complete" && r.Method == http.MethodPost:
		if op.ProductComplete {
			writeFreshJSON(w, op)
			return
		}
		next, err := a.store.Update(r.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
			if record.Operation.Strategy.Stage != "committed" {
				return freshstore.ErrConflict
			}
			record.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		writeFreshJSON(w, next.Operation)
	default:
		http.NotFound(w, r)
	}
}

func (a *freshApplication) fixedStrategy(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.executeStrategy(w, r, op)
}

func (a *freshApplication) executeStrategy(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	var request struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || request.Identity != op.Identity || request.ExpectedRevision != op.Revision {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := a.validateCommittedExternalReplay(r.Context(), op, request.Request); err != nil {
		writeFreshStrategyError(w, err)
		return
	}
	if request.Strategy == bootstrap.AdoptExisting {
		record, err := a.store.Load(r.Context())
		if err != nil || record.Operation.Identity != op.Identity || record.Operation.Revision != op.Revision {
			w.WriteHeader(http.StatusConflict)
			return
		}
		request.Request.AdoptionCiphertext = record.AdoptionCiphertext
	}
	result, err := a.strategy.Execute(r.Context(), op, request.Request)
	if err != nil {
		writeFreshStrategyError(w, err)
		return
	}
	if token, fixed := freshbootstrap.SessionToken(r); fixed {
		current, err := a.authority.Authorize(r.Context(), token)
		if err != nil || current.Identity != op.Identity || current.Revision != op.Revision {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if op.Strategy.Stage == "committed" {
		// Reconcile only after the strategy adapter validates the requested
		// target and current server-owned qualification. Do not rewrite a
		// committed checkpoint, connection ciphertext or revision on replay.
		writeFreshJSON(w, op)
		return
	}
	next, err := a.store.Update(r.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
		if token, fixed := freshbootstrap.SessionToken(r); fixed {
			if err := a.authority.ValidateSession(*record, token); err != nil {
				return err
			}
		}
		record.Operation.Strategy = result.Checkpoint
		if result.AdoptionCiphertext != "" {
			record.AdoptionCiphertext = result.AdoptionCiphertext
		}
		if result.Connection != nil {
			record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: result.Connection.BaseURL, ManagementKey: result.Connection.ManagementKeyCiphertext}
		}
		return nil
	})
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		return
	}
	writeFreshJSON(w, next.Operation)
}

func writeFreshJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func freshDriver(cfg config.Config) freshrouting.Driver {
	driver, err := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{
		IngressControlURL: strings.TrimSpace(os.Getenv("CPAMP_INGRESS_CONTROL_URL")),
		PublicOrigin:      strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
	})
	if err != nil {
		return nil
	}
	return driver
}

func restoreFreshRouting(ctx context.Context, service *freshrouting.Service) {
	// Ingress is stateless. Re-deliver only the original persisted projection
	// after an independent Ingress restart; this never advances DB authority.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = service.Restore(probe)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func freshMarkerPresent(database string) bool {
	info, err := os.Lstat(database + ".manager.lock")
	// An ordinary historical empty marker is not Fresh evidence. Any bounded
	// non-empty marker requires strict parsing by admission and fails closed.
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func serveExistingFresh(ctx context.Context, cfg config.Config, paths sqliterepo.InspectionPaths, h *startupHTTPHandler) (*fresh.Opened, error) {
	if err := validateFreshHostAuthority(cfg); err != nil {
		return nil, err
	}
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{AdminKey: cfg.AdminKey, DataKey: cfg.DataKey})
	if err != nil {
		return nil, err
	}
	app := newFreshApplication(opened, cfg, freshDriver(cfg))
	h.SetFreshHandler(app)
	go restoreFreshRouting(ctx, app.routing)
	return opened, nil
}

func validateFreshHostAuthority(cfg config.Config) error {
	if cfg.AdminAuthorityConfigured && cfg.AdminKey == "" {
		return fresh.ErrAuthority
	}
	return nil
}

func awaitFreshStore(ctx context.Context, cfg config.Config, paths sqliterepo.InspectionPaths, h *startupHTTPHandler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !freshMarkerPresent(paths.Database) {
			continue
		}
		opened, err := serveExistingFresh(ctx, cfg, paths, h)
		if err != nil {
			continue
		}
		<-ctx.Done()
		_ = opened.Close()
		return
	}
}
