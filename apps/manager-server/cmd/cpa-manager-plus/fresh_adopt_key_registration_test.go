package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	freshkeyssvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// adoptedClientKeys are the source CPA's client keys of this fixture. They carry
// none of this Manager's canonical shapes, exactly like a real adopted CPA.
var adoptedClientKeys = []string{"source-client-two", "source-client-one"}

// adoptionFixtureWithKeys stages the full adoption with the source presenting
// these client keys, wires the Managed gateway/plugin configuration surfaces,
// and runs the migration to a settled run.
func adoptionFixtureWithKeys(t *testing.T, clientKeys ...string) *adoptMigrationFixture {
	t.Helper()
	fixture := newAdoptMigrationFixtureWithClientKeys(t, clientKeys...)
	wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	if summary := fixture.reload(t).AdoptionSummary; summary == nil || !summary.Finished {
		t.Fatalf("the migration did not record a finished run: %+v", summary)
	}
	return fixture
}

type activeAdoptRoutingDriver struct {
	store freshstore.Store
}

func (d activeAdoptRoutingDriver) Inspect(ctx context.Context) (freshrouting.State, error) {
	record, err := d.store.Load(ctx)
	if err != nil {
		return freshrouting.State{}, err
	}
	return freshrouting.StateFor(record.Operation), nil
}

func (activeAdoptRoutingDriver) Apply(context.Context, freshrouting.State) error  { return nil }
func (activeAdoptRoutingDriver) Verify(context.Context, freshrouting.State) error { return nil }

func wireAdoptManagedAuthority(app *freshApplication, rt *migrationRuntime, destinationURL string) {
	storeView := freshStoreView{app: app}
	source := managedInstallSource(storeView, app.protector, destinationURL)
	app.managedSource = source
	app.runtime = rt
	managedAuthority := freshkeyapply.NewManaged(app.keyRepo, app.keys, gatedEmbeddedRuntimeClient{app: app}, destinationURL, source)
	externalSource := externalInstallSource(storeView, app.protector)
	app.externalSource = externalSource
	externalAuthority := freshkeyapply.NewExternal(app.keyRepo, app.keys, externalSource)
	app.keyApply = freshkeyapply.NewStrategySelector(
		func(op bootstrap.FreshOperation) *freshkeyapply.Service {
			switch {
			case isCommittedExternalStrategy(op.Strategy):
				return externalAuthority
			case freshkeyapply.ManagerOwnedStrategy(op.Strategy):
				return managedAuthority
			default:
				return nil
			}
		},
		func() (bootstrap.FreshOperation, bool) {
			op, loadErr := app.current(context.Background())
			return op, loadErr == nil
		},
	)
	app.gatewayConfig = freshconfig.New(storeView, app.keyApply.ConfigTarget,
		app.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
	app.gatewayConfig.EnableManaged(destinationURL)
	app.gatewayConfig.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		install, err := source(context.Background())
		if err != nil {
			return cpa.DestinationPosture{}, false
		}
		return cpa.PostureFor(install.Strategy)
	})
	app.gatewayConfig.EnableExternal(app.keyApply.ExternalTarget)
	app.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, app.v8PluginCapable)
	app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
}

func serveHTTPRequest(t *testing.T, app *freshApplication, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// Completion is where an adopted destination's client keys become the Key
// workspace's records and the install journal is claimed, so the credentials the
// console listed as migrated are the credentials it can now revoke or replace.
func TestAdoptCompletionRegistersTheDestinationKeys(t *testing.T) {
	fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
	ctx := t.Context()

	if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("the installation listed keys before completion: %#v %v", listed, err)
	}
	if response := fixture.complete(t); response.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", response.Code, response.Body.String())
	}
	keys, err := fixture.app.keys.List(ctx)
	if err != nil || len(keys) != len(adoptedClientKeys) {
		t.Fatalf("workspace list = %#v %v", keys, err)
	}
	for index, key := range keys {
		if key.Origin != freshkeys.OriginAdopted || key.Lifecycle != "active" {
			t.Fatalf("key %d is not an adopted record: %#v", index, key)
		}
		if key.MaskedPreview != "…"+adoptedClientKeys[index][len(adoptedClientKeys[index])-4:] {
			t.Fatalf("key %d preview = %q", index, key.MaskedPreview)
		}
		if key.NameRevision != 1 || !slices.Contains([]string{"not_applied", "effect_unknown"}, key.RuntimeState) {
			t.Fatalf("key %d is not a fresh installation record: %#v", index, key)
		}
	}
	journal, err := fixture.app.keyRepo.InitialInstall(ctx)
	if err != nil || journal == nil {
		t.Fatalf("install journal: %#v %v", journal, err)
	}
	if journal.KeyID != string(keys[0].ID) || journal.RunID != freshkeyapply.ManagedRunID(string(fixture.record.Operation.Identity.OperationID)) ||
		journal.Generation != "3" {
		t.Fatalf("journal is not bound to this installation: %#v", journal)
	}
	var activeBindings int
	if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null`).Scan(&activeBindings); err != nil || activeBindings != len(adoptedClientKeys) {
		t.Fatalf("active source bindings after completion = %d (%v), want %d", activeBindings, err, len(adoptedClientKeys))
	}

	// The console's own list is the surface the walkthrough found empty: it now
	// answers with the destination's credentials and never with their plaintext.
	response := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("api-keys after completion: %d %s", response.Code, response.Body.String())
	}
	var listed struct {
		Keys []struct {
			ID            string `json:"id"`
			Origin        string `json:"origin"`
			MaskedPreview string `json:"maskedPreview"`
			Lifecycle     string `json:"lifecycle"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed.Keys) != len(keys) {
		t.Fatalf("api-keys body: %s", response.Body.String())
	}
	for index, key := range listed.Keys {
		if key.ID != string(keys[index].ID) || key.Origin != freshkeys.OriginAdopted || key.Lifecycle != "active" ||
			key.MaskedPreview != keys[index].MaskedPreview {
			t.Fatalf("api-keys entry %d = %#v", index, key)
		}
		if strings.Contains(response.Body.String(), adoptedClientKeys[index]) {
			t.Fatalf("api-keys disclosed %q", adoptedClientKeys[index])
		}
	}

	// A replay of an already-complete installation is idempotent: it answers 200
	// and registers nothing a second time.
	completed := fixture.reload(t)
	if replay := fixture.complete(t); replay.Code != http.StatusOK {
		t.Fatalf("completion replay: %d %s", replay.Code, replay.Body.String())
	}
	if after, err := fixture.app.keys.List(ctx); err != nil || len(after) != len(keys) {
		t.Fatalf("completion replay changed the key list: %#v %v", after, err)
	}
	if again, err := fixture.app.keyRepo.InitialInstall(ctx); err != nil || again == nil || *again != *journal {
		t.Fatalf("completion replay rewrote the journal: %#v %v", again, err)
	}
	if fixture.reload(t).Operation.Revision != completed.Operation.Revision {
		t.Fatal("completion replay changed the installation record")
	}
}

type loadFailingStore struct {
	freshstore.Store
	failFromCall       int32
	failOnlyCall       int32
	failUntilCall      int32
	calls              atomic.Int32
	err                error
	beforeWithRecordTx func()
}

func (s *loadFailingStore) Load(ctx context.Context) (freshstore.Record, error) {
	n := s.calls.Add(1)
	if s.failOnlyCall > 0 && n == s.failOnlyCall {
		return freshstore.Record{}, s.err
	}
	if s.failUntilCall > 0 && n >= s.failFromCall && n <= s.failUntilCall {
		return freshstore.Record{}, s.err
	}
	if s.failUntilCall == 0 && s.failFromCall > 0 && n >= s.failFromCall {
		return freshstore.Record{}, s.err
	}
	return s.Store.Load(ctx)
}

func (s *loadFailingStore) UpdateTx(ctx context.Context, id bootstrap.FreshIdentity, rev uint64, fn func(*freshstore.Record, *sql.Tx) error) (freshstore.Record, error) {
	return s.Store.(freshstore.TransactionalUpdater).UpdateTx(ctx, id, rev, fn)
}

func (s *loadFailingStore) WithRecordTx(ctx context.Context, fn func(freshstore.Record, *sql.Tx) error) error {
	if s.beforeWithRecordTx != nil {
		s.beforeWithRecordTx()
	}
	return s.Store.(freshstore.TransactionalUpdater).WithRecordTx(ctx, fn)
}

type updateTxHookStore struct {
	freshstore.Store
	beforeTx func(context.Context, freshstore.Store) error
	afterFn  func() error
}

func (s *updateTxHookStore) Update(ctx context.Context, id bootstrap.FreshIdentity, rev uint64, fn func(*freshstore.Record) error) (freshstore.Record, error) {
	return s.UpdateTx(ctx, id, rev, func(record *freshstore.Record, _ *sql.Tx) error {
		return fn(record)
	})
}

func (s *updateTxHookStore) UpdateTx(ctx context.Context, id bootstrap.FreshIdentity, rev uint64, fn func(*freshstore.Record, *sql.Tx) error) (freshstore.Record, error) {
	if s.beforeTx != nil {
		if err := s.beforeTx(ctx, s.Store); err != nil {
			return freshstore.Record{}, err
		}
	}
	return s.Store.(freshstore.TransactionalUpdater).UpdateTx(ctx, id, rev, func(record *freshstore.Record, tx *sql.Tx) error {
		if err := fn(record, tx); err != nil {
			return err
		}
		if s.afterFn != nil {
			return s.afterFn()
		}
		return nil
	})
}

func (s *updateTxHookStore) WithRecordTx(ctx context.Context, fn func(freshstore.Record, *sql.Tx) error) error {
	return s.Store.(freshstore.TransactionalUpdater).WithRecordTx(ctx, fn)
}

func readGatewayConfigView(t *testing.T, app *freshApplication) freshconfig.View {
	t.Helper()
	response := gatewayRequest(t, app, http.MethodGet, "gateway/config", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET gateway/config status=%d body=%s", response.Code, response.Body.String())
	}
	var view freshconfig.View
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode gateway/config: %v", err)
	}
	return view
}

func groupBaseline(t *testing.T, view freshconfig.View, groupID string) string {
	t.Helper()
	for _, group := range view.Groups {
		if group.ID == groupID {
			return group.Baseline
		}
	}
	t.Fatalf("missing config group %q in %+v", groupID, view.Groups)
	return ""
}

func fieldValue(t *testing.T, view freshconfig.View, path string) any {
	t.Helper()
	for _, field := range view.Fields {
		if field.Path == path {
			return field.Value
		}
	}
	t.Fatalf("missing config field %q in %+v", path, view.Fields)
	return nil
}

func assertNoAdoptWriteAuthority(t *testing.T, fixture *adoptMigrationFixture, targetKeyID, keeperKeyID string) {
	t.Helper()
	if fixture.app.gatewayAdmissionProven(t.Context()) {
		t.Fatal("incomplete adoption proved Gateway admission")
	}
	cfgSave := gatewayRequest(t, fixture.app, http.MethodPost, "gateway/config/groups/routing",
		`{"requestId":"`+strings.Repeat("ab", 16)+`","expected":{"generation":"3","baseline":"`+strings.Repeat("f", 64)+`"},"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`,
		"application/json")
	if cfgSave.Code != http.StatusConflict || errorCode(t, cfgSave) != freshconfig.ReasonUnavailable {
		t.Fatalf("incomplete adoption gateway config write = %d %s", cfgSave.Code, cfgSave.Body.String())
	}
	if got := fixture.destination.value("routing", "strategy"); got != "round-robin" {
		t.Fatalf("incomplete adoption mutated destination routing.strategy = %#v", got)
	}

	pluginSave := gatewayRequest(t, fixture.app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody("3", strings.Repeat("f", 64), strings.Repeat("cd", 16), true), "application/json")
	if pluginSave.Code != http.StatusConflict || errorCode(t, pluginSave) != freshconfig.ReasonUnavailable {
		t.Fatalf("incomplete adoption plugin switch write = %d %s", pluginSave.Code, pluginSave.Body.String())
	}
	if got := fixture.destination.value("plugins", "enabled"); got != false {
		t.Fatalf("incomplete adoption mutated destination plugins.enabled = %#v", got)
	}

	revoke := gatewayRequest(t, fixture.app, http.MethodPost, "api-keys/"+targetKeyID+"/revocation",
		`{"requestId":"`+strings.Repeat("ef", 16)+`","keeperId":"`+keeperKeyID+`","mode":"revoke"}`, "application/json")
	if revoke.Code != http.StatusConflict {
		t.Fatalf("incomplete adoption key-operation POST = %d %s", revoke.Code, revoke.Body.String())
	}
	if got := fixture.destination.clientKeys(); len(got) != len(adoptedClientKeys) {
		t.Fatalf("incomplete adoption mutated destination client keys = %#v", got)
	}
}

// Every completion conflict (409) and decryption/storage failure (503) returns
// the agreed structured error code and leaves the installation all-or-nothing:
// neither workspace keys, nor source bindings, nor the install journal remain.
func TestAdoptCompletionRefusesConflictsAndStorageFailuresAtomically(t *testing.T) {
	t.Run("foreign install journal rolls back all adopted keys and bindings", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		if err := fixture.app.keyRepo.ClaimInitialInstall(ctx, freshkeys.InitialInstall{
			KeyID: strings.Repeat("a", 32), RunID: "managed:another-install", Generation: "3", AttemptedAtMS: 1,
		}); err != nil {
			t.Fatal(err)
		}
		response := fixture.complete(t)
		if response.Code != http.StatusConflict || errorCode(t, response) != "adopt_installation_conflict" {
			t.Fatalf("foreign journal completion: %d %s", response.Code, response.Body.String())
		}
		if fixture.reload(t).Operation.ProductComplete {
			t.Fatal("a refused registration completed the installation")
		}
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("refused registration left workspace keys %#v %v", listed, err)
		}
		var identities, bindings int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_identities`).Scan(&identities); err != nil || identities != 0 {
			t.Fatalf("refused registration left %d identities: %v", identities, err)
		}
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings`).Scan(&bindings); err != nil || bindings != 0 {
			t.Fatalf("refused registration left %d source bindings: %v", bindings, err)
		}
		journal, err := fixture.app.keyRepo.InitialInstall(ctx)
		if err != nil || journal == nil || journal.RunID != "managed:another-install" {
			t.Fatalf("foreign journal changed = %#v %v", journal, err)
		}
	})

	t.Run("retired nth destination key leaves zero earlier keys", func(t *testing.T) {
		fixture := newAdoptMigrationFixtureWithClientKeys(t, "source-valid-first", "source-retired-second")
		wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
		ctx := t.Context()
		retired, err := fixture.app.keys.RegisterAdopted(ctx, []string{"source-retired-second"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.opened.DB.Exec(`update gateway_api_key_identities set lifecycle='superseded', revision=revision+1 where id=?`, string(retired[0].ID)); err != nil {
			t.Fatal(err)
		}
		if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
			t.Fatalf("commit: %d %s", response.Code, response.Body.String())
		}
		fixture.wait()

		var bindingsBefore int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsBefore); err != nil {
			t.Fatal(err)
		}
		response := fixture.complete(t)
		if response.Code != http.StatusConflict || errorCode(t, response) != "adopt_installation_conflict" {
			t.Fatalf("complete = %d %s", response.Code, response.Body.String())
		}
		listed, err := fixture.app.keys.List(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != retired[0].ID {
			t.Fatalf("nth-key refusal left earlier key behind: %#v %v", listed, err)
		}
		var bindingsAfter int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsAfter); err != nil || bindingsAfter != bindingsBefore {
			t.Fatalf("nth-key refusal changed source bindings %d -> %d: %v", bindingsBefore, bindingsAfter, err)
		}
		if journal, err := fixture.app.keyRepo.InitialInstall(ctx); err != nil || journal != nil {
			t.Fatalf("nth-key refusal wrote journal: %#v %v", journal, err)
		}
	})

	t.Run("missing or mismatched protector returns 503 adopt_installation_unavailable", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		originalProtector := fixture.app.protector

		fixture.app.protector = nil
		nilResp := fixture.complete(t)
		if nilResp.Code != http.StatusServiceUnavailable || errorCode(t, nilResp) != "adopt_installation_unavailable" {
			t.Fatalf("nil protector complete = %d %s", nilResp.Code, nilResp.Body.String())
		}

		wrongProtector, err := security.NewProtector([]byte("mismatched-adoption-protector-key"))
		if err != nil {
			t.Fatal(err)
		}
		fixture.app.protector = wrongProtector
		badResp := fixture.complete(t)
		if badResp.Code != http.StatusServiceUnavailable || errorCode(t, badResp) != "adopt_installation_unavailable" {
			t.Fatalf("mismatched protector complete = %d %s", badResp.Code, badResp.Body.String())
		}
		fixture.app.protector = originalProtector
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("protector failure left keys: %#v %v", listed, err)
		}
	})

	t.Run("store Load or transactional write failure returns 503 adopt_installation_unavailable and rolls back", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		realStore := fixture.app.store
		fixture.app.pluginCapability = nil

		// Outermost first Load failure (`failFromCall: 1`) through both ServeHTTP
		// (`canonical`) and `canonicalAPI`, plus inner Load failure (`failFromCall: 2`),
		// must all return 503 `adopt_installation_unavailable`, while `ErrNotFresh`
		// still answers 404.
		for _, failAt := range []int32{1, 2} {
			failing := &loadFailingStore{
				Store:        realStore,
				failFromCall: failAt,
				err:          errors.New("simulated sqlite read outage"),
			}
			fixture.app.store = failing
			fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
			serveResp := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
			if serveResp.Code != http.StatusServiceUnavailable || errorCode(t, serveResp) != "adopt_installation_unavailable" {
				t.Fatalf("ServeHTTP store.Load outage (failFromCall=%d) = %d %s", failAt, serveResp.Code, serveResp.Body.String())
			}

			failing.calls.Store(0)
			apiResp := fixture.complete(t)
			if apiResp.Code != http.StatusServiceUnavailable || errorCode(t, apiResp) != "adopt_installation_unavailable" {
				t.Fatalf("canonicalAPI store.Load outage (failFromCall=%d) = %d %s", failAt, apiResp.Code, apiResp.Body.String())
			}
		}

		// One-shot transient failure on call #2 (`a.routing.Snapshot` inside
		// `canonical`) followed by immediate recovery must serve the recovered
		// committed route instead of returning a spurious 404.
		fixture.app.store = &loadFailingStore{
			Store:        realStore,
			failOnlyCall: 2,
			err:          errors.New("transient snapshot sqlite read blip"),
		}
		fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
		recoveredOp := serveHTTPRequest(t, fixture.app, http.MethodGet, "/control/api/v2/operation", "", "")
		if recoveredOp.Code != http.StatusOK {
			t.Errorf("transient Snapshot blip on committed route = %d %s, want 200", recoveredOp.Code, recoveredOp.Body.String())
		}

		// If `a.routing.Snapshot` remains unavailable across retry while a subsequent
		// `a.store.Load` confirms `Routing.Committed` is still true, `canonical`
		// must return 503 rather than misreporting 404.
		fixture.app.store = &loadFailingStore{
			Store:         realStore,
			failFromCall:  2,
			failUntilCall: 3,
			err:           errors.New("transient snapshot unavailability"),
		}
		fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
		stillCommittedUnavailable := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if stillCommittedUnavailable.Code != http.StatusServiceUnavailable || errorCode(t, stillCommittedUnavailable) != "adopt_installation_unavailable" {
			t.Fatalf("SnapshotUnavailable with committed reload = %d %s, want 503 adopt_installation_unavailable", stillCommittedUnavailable.Code, stillCommittedUnavailable.Body.String())
		}

		fixture.app.store = &loadFailingStore{
			Store:        realStore,
			failFromCall: 1,
			err:          freshstore.ErrNotFresh,
		}
		fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
		if notFresh := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json"); notFresh.Code != http.StatusNotFound {
			t.Fatalf("ErrNotFresh complete = %d, want 404", notFresh.Code)
		}

		fixture.app.store = &updateTxHookStore{
			Store:   realStore,
			afterFn: func() error { return errors.New("simulated sqlite commit outage") },
		}
		fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
		txResp := fixture.complete(t)
		if txResp.Code != http.StatusServiceUnavailable || errorCode(t, txResp) != "adopt_installation_unavailable" {
			t.Fatalf("UpdateTx outage = %d %s", txResp.Code, txResp.Body.String())
		}
		fixture.app.store = realStore
		fixture.app.routing = freshrouting.New(fixture.app.store, activeAdoptRoutingDriver{store: fixture.app.store})
		if fixture.reload(t).Operation.ProductComplete {
			t.Fatal("storage failure completed the installation")
		}
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("storage failure left keys: %#v %v", listed, err)
		}
		if journal, err := fixture.app.keyRepo.InitialInstall(ctx); err != nil || journal != nil {
			t.Fatalf("storage failure left journal: %#v %v", journal, err)
		}
	})
}

// When the completion CAS rejects (because a concurrent writer advanced the
// Fresh revision or invalidated the continuation), completion grants no write
// authority and leaves no adopted keys or journal behind. Even if a journal
// row exists while `ProductComplete` is false, all write surfaces fail closed.
func TestAdoptCompletionCASFailureGrantsNoWriteAuthorityAndLeavesNoKeys(t *testing.T) {
	t.Run("CAS conflict rolls back registration and leaves zero write authority", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		realStore := fixture.app.store

		fixture.app.store = &updateTxHookStore{
			Store: realStore,
			beforeTx: func(ctx context.Context, store freshstore.Store) error {
				loaded, err := store.Load(ctx)
				if err != nil {
					return err
				}
				_, err = store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
					r.Authority.SessionGeneration++
					return nil
				})
				return err
			},
		}
		response := fixture.complete(t)
		fixture.app.store = realStore
		if response.Code != http.StatusConflict {
			t.Fatalf("CAS-rejected complete = %d %s", response.Code, response.Body.String())
		}
		if fixture.reload(t).Operation.ProductComplete {
			t.Fatal("CAS conflict completed the installation")
		}
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("CAS conflict left workspace keys = %#v %v", listed, err)
		}
		if journal, err := fixture.app.keyRepo.InitialInstall(ctx); err != nil || journal != nil {
			t.Fatalf("CAS conflict left initial-install journal = %#v %v", journal, err)
		}

		stagedA, _, err := fixture.app.keys.Create(ctx, strings.Repeat("1", 32))
		if err != nil {
			t.Fatal(err)
		}
		stagedB, _, err := fixture.app.keys.Create(ctx, strings.Repeat("2", 32))
		if err != nil {
			t.Fatal(err)
		}
		assertNoAdoptWriteAuthority(t, fixture, string(stagedA.Key.ID), string(stagedB.Key.ID))
	})

	t.Run("consumer fence refuses writes while ProductComplete is false even with a journal", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		keys, err := fixture.app.keyApply.RegisterAdoption(t.Context(), adoptedClientKeys)
		if err != nil {
			t.Fatal(err)
		}
		assertNoAdoptWriteAuthority(t, fixture, string(keys[1].ID), string(keys[0].ID))
	})
}

// Completing an Adopt installation enables real gateway configuration saves and
// plugin runtime switches through the HTTP API, persists those edits on the
// destination, and preserves key listing/copying, config write authority and
// key revocation across a Manager restart.
func TestAdoptCompletionEnablesRealConfigAndPluginWritesAcrossRestart(t *testing.T) {
	fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
	ctx := t.Context()

	beforeCfg := readGatewayConfigView(t, fixture.app)
	if beforeCfg.Capability.Mode != freshconfig.ModeUnavailable || beforeCfg.Capability.Writable {
		t.Fatalf("pre-completion gateway capability = %+v", beforeCfg.Capability)
	}
	beforePlugin := readPluginRuntime(t, fixture.app)
	if beforePlugin.Capability.Mode != freshconfig.ModeUnavailable || beforePlugin.Capability.Reason != freshconfig.ReasonUnavailable || beforePlugin.Capability.Writable {
		t.Fatalf("pre-completion plugin capability = %+v", beforePlugin.Capability)
	}
	assertNoAdoptWriteAuthority(t, fixture, strings.Repeat("a", 32), strings.Repeat("b", 32))
	// Exercise the unmodified production async plugin capability probe through
	// `app.ServeHTTP`, including an old-epoch pre-completion probe held open
	// across completion: pre-completion responses carry no plugin header, while
	// the VERY FIRST successful Console response after completion carries
	// `X-CPA-SUPPORT-PLUGIN: 1`.
	oldProbeEntered := make(chan struct{})
	releaseOldProbe := make(chan struct{})
	var probeCalls atomic.Int32
	fixture.app.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, func(probeCtx context.Context) bool {
		if probeCalls.Add(1) == 1 {
			close(oldProbeEntered)
			<-releaseOldProbe
			return false
		}
		return fixture.app.v8PluginCapable(probeCtx)
	})
	preOpResp := serveHTTPRequest(t, fixture.app, http.MethodGet, "/control/api/v2/operation", "", "")
	if preOpResp.Code != http.StatusOK || preOpResp.Header().Get(supportPluginHeader) != "" {
		t.Fatalf("pre-completion ServeHTTP operation = %d header=%q", preOpResp.Code, preOpResp.Header().Get(supportPluginHeader))
	}
	<-oldProbeEntered

	completeResp := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
	if completeResp.Code != http.StatusOK {
		t.Fatalf("ServeHTTP complete = %d %s", completeResp.Code, completeResp.Body.String())
	}
	close(releaseOldProbe)

	firstPostComplete := serveHTTPRequest(t, fixture.app, http.MethodGet, "/control/api/v2/api-keys", "", "")
	if firstPostComplete.Code != http.StatusOK || firstPostComplete.Header().Get(supportPluginHeader) != supportPluginHeaderValue {
		t.Fatalf("first post-completion Console response = %d %s=%q", firstPostComplete.Code, supportPluginHeader, firstPostComplete.Header().Get(supportPluginHeader))
	}
	if got := completeResp.Header().Get(supportPluginHeader); got != supportPluginHeaderValue {
		t.Fatalf("ServeHTTP complete %s = %q, want %q", supportPluginHeader, got, supportPluginHeaderValue)
	}
	// 1. Gateway configuration: perform a real group save and read back.
	cfgView := readGatewayConfigView(t, fixture.app)
	if cfgView.Capability.Mode != freshconfig.ModeManaged || !cfgView.Capability.Writable {
		t.Fatalf("post-completion gateway capability = %+v", cfgView.Capability)
	}
	if got := fieldValue(t, cfgView, "routing.strategy"); got != "round-robin" {
		t.Fatalf("initial routing.strategy = %#v", got)
	}
	routingBaseline := groupBaseline(t, cfgView, "routing")
	cfgSaveBody := `{"requestId":"` + strings.Repeat("11", 16) + `","expected":{"generation":"` + cfgView.Generation + `","baseline":"` + routingBaseline + `"},"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`
	cfgSave := gatewayRequest(t, fixture.app, http.MethodPost, "gateway/config/groups/routing", cfgSaveBody, "application/json")
	if cfgSave.Code != http.StatusOK {
		t.Fatalf("POST gateway/config/groups/routing = %d %s", cfgSave.Code, cfgSave.Body.String())
	}
	cfgAfter := readGatewayConfigView(t, fixture.app)
	if got := fieldValue(t, cfgAfter, "routing.strategy"); got != "fill-first" {
		t.Fatalf("routing.strategy after save = %#v", got)
	}
	if got := fixture.destination.value("routing", "strategy"); got != "fill-first" {
		t.Fatalf("destination routing.strategy = %#v", got)
	}

	// 2. Plugin runtime switch: perform a real toggle and read back.
	pluginView := readPluginRuntime(t, fixture.app)
	if pluginView.Capability.Mode != freshconfig.ModeManaged || !pluginView.Capability.Writable {
		t.Fatalf("post-completion plugin capability = %+v", pluginView.Capability)
	}
	if got, ok := pluginView.Fields[0].Value.(bool); !ok || got {
		t.Fatalf("initial plugins.enabled = %#v", pluginView.Fields[0].Value)
	}
	pluginSave := gatewayRequest(t, fixture.app, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody(pluginView.Generation, pluginView.Groups[0].Baseline, strings.Repeat("22", 16), true), "application/json")
	if pluginSave.Code != http.StatusOK {
		t.Fatalf("POST plugins/runtime/groups/plugins = %d %s", pluginSave.Code, pluginSave.Body.String())
	}
	pluginAfter := readPluginRuntime(t, fixture.app)
	if got, ok := pluginAfter.Fields[0].Value.(bool); !ok || !got {
		t.Fatalf("plugins.enabled after save = %#v", pluginAfter.Fields[0].Value)
	}
	if got := fixture.destination.value("plugins", "enabled"); got != true {
		t.Fatalf("destination plugins.enabled = %#v", got)
	}

	// 3. Restart: build a brand-new freshApplication over the same SQLite DB &
	// Protector with zero in-memory carryover from the completion request.
	restarted := newFreshApplication(fixture.opened, config.Config{}, nil)
	wireAdoptManagedAuthority(restarted, fixture.runtime, fixture.destination.URL())
	if !restarted.gatewayAdmissionProven(ctx) {
		t.Fatal("restarted Manager lost Gateway admission for completed adoption")
	}
	restartedCfg := readGatewayConfigView(t, restarted)
	if !restartedCfg.Capability.Writable || fieldValue(t, restartedCfg, "routing.strategy") != "fill-first" {
		t.Fatalf("restarted gateway config = %+v", restartedCfg)
	}
	restartedPlugin := readPluginRuntime(t, restarted)
	if !restartedPlugin.Capability.Writable {
		t.Fatalf("restarted plugin capability = %+v", restartedPlugin.Capability)
	}
	toggleBack := gatewayRequest(t, restarted, http.MethodPost, "plugins/runtime/groups/plugins",
		pluginSwitchBody(restartedPlugin.Generation, restartedPlugin.Groups[0].Baseline, strings.Repeat("33", 16), false), "application/json")
	if toggleBack.Code != http.StatusOK {
		t.Fatalf("restarted plugin write = %d %s", toggleBack.Code, toggleBack.Body.String())
	}
	if got := fixture.destination.value("plugins", "enabled"); got != false {
		t.Fatalf("destination plugins.enabled after restart write = %#v", got)
	}

	listed, err := restarted.keys.List(ctx)
	if err != nil || len(listed) != len(adoptedClientKeys) {
		t.Fatalf("restarted keys = %#v %v", listed, err)
	}
	copyResp := gatewayRequest(t, restarted, http.MethodPost, "api-keys/"+string(listed[1].ID)+"/secret", `{}`, "application/json")
	if copyResp.Code != http.StatusOK || copyResp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("restarted copy = %d headers=%v body=%s", copyResp.Code, copyResp.Header(), copyResp.Body.String())
	}
	var copied struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(copyResp.Body.Bytes(), &copied); err != nil || copied.Secret != adoptedClientKeys[1] {
		t.Fatalf("restarted copied secret = %#v %v", copied, err)
	}

	prepared := gatewayRequest(t, restarted, http.MethodPost, "api-keys/"+string(listed[1].ID)+"/revocation",
		`{"requestId":"`+strings.Repeat("44", 16)+`","keeperId":"`+string(listed[0].ID)+`","mode":"revoke"}`, "application/json")
	if prepared.Code != http.StatusOK {
		t.Fatalf("restarted prepare revoke = %d %s", prepared.Code, prepared.Body.String())
	}
	var op freshkeyapply.OperationResult
	if err := json.Unmarshal(prepared.Body.Bytes(), &op); err != nil || op.Operation == nil {
		t.Fatalf("decode prepare revoke: %#v %v", op, err)
	}
	committed := gatewayRequest(t, restarted, http.MethodPost, "api-keys/operations/"+op.Operation.ID+"/continue", `{}`, "application/json")
	if committed.Code != http.StatusOK {
		t.Fatalf("restarted continue revoke = %d %s", committed.Code, committed.Body.String())
	}
	if err := json.Unmarshal(committed.Body.Bytes(), &op); err != nil || op.Operation == nil || op.Operation.Outcome != "committed" || op.RuntimeState != "effective_observed" {
		t.Fatalf("restarted committed revoke = %#v (%v)", op, err)
	}
	if got := fixture.destination.clientKeys(); len(got) != 1 || got[0] != adoptedClientKeys[0] {
		t.Fatalf("destination client keys after restarted revoke = %#v", got)
	}
}

// When the adopted CPA carries a Client Key that was already created in the
// Manager workspace before strategy commit (without a source binding), adopt
// completion binds its source runtime inside the same transaction; if an
// existing key is already bound to a different runtime, completion refuses
// with 409 and rolls back every sibling key and binding.
func TestAdoptCompletionReusesPreStagedWorkspaceKeysAndBindsSourceAtomically(t *testing.T) {
	t.Run("pre-staged workspace key without binding is bound in the completion transaction", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		unwiredKeys := freshkeyssvc.New(fixture.app.keyRepo, fixture.app.protector)
		staged, _, err := unwiredKeys.Create(ctx, strings.Repeat("7", 32))
		if err != nil {
			t.Fatal(err)
		}
		var bindingsBefore int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null`).Scan(&bindingsBefore); err != nil || bindingsBefore != 0 {
			t.Fatalf("bindings before completion = %d (%v), want 0", bindingsBefore, err)
		}
		fixture.destination.mu.Lock()
		fixture.destination.config["access"] = map[string]any{"api-keys": []any{staged.CreatedSecret, "source-peer-two"}}
		fixture.destination.mu.Unlock()

		resp := fixture.complete(t)
		if resp.Code != http.StatusOK {
			t.Fatalf("complete = %d %s", resp.Code, resp.Body.String())
		}
		var activeBindings int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null and runtime_identity='runtime'`).Scan(&activeBindings); err != nil || activeBindings != 2 {
			t.Fatalf("active runtime bindings after completion = %d (%v), want 2", activeBindings, err)
		}
	})

	t.Run("existing key bound to incompatible runtime refuses 409 and rolls back sibling keys", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, "source-peer-clean", "source-peer-foreign-runtime")
		ctx := t.Context()
		staged, err := fixture.app.keys.RegisterAdopted(ctx, []string{"source-peer-foreign-runtime"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.opened.DB.Exec(`update gateway_api_key_source_bindings set runtime_identity='external-other' where api_key_id=?`, string(staged[0].ID)); err != nil {
			t.Fatal(err)
		}

		resp := fixture.complete(t)
		if resp.Code != http.StatusConflict || errorCode(t, resp) != "adopt_installation_conflict" {
			t.Fatalf("foreign-runtime complete = %d %s", resp.Code, resp.Body.String())
		}
		if fixture.reload(t).Operation.ProductComplete {
			t.Fatal("foreign-runtime conflict completed the installation")
		}
		listed, err := fixture.app.keys.List(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != staged[0].ID {
			t.Fatalf("foreign-runtime conflict leaked sibling key: %#v %v", listed, err)
		}
		var runtimeBindings int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where runtime_identity='runtime'`).Scan(&runtimeBindings); err != nil || runtimeBindings != 0 {
			t.Fatalf("foreign-runtime conflict left %d runtime bindings: %v", runtimeBindings, err)
		}
	})
}

// An Adopt installation that reached `ProductComplete=true` on a build prior to
// journaled adoption has neither adopted workspace keys nor an install journal:
//  1. before backfill, Gateway admission, config saves, plugin toggles and key
//     operations all fail closed consistently (no admission without a journal);
//  2. if backfill meets a foreign journal or an unproven destination, `POST
//     complete` refuses with 409/503 instead of returning a silent 200; and
//  3. `prepareCompletedStartup` (on Manager restart) or `POST complete`
//     atomically backfills the adopted keys, source bindings and journal once
//     without bumping `Operation.Revision`, restoring full authority.
func TestHistoricalCompletedAdoptionFailsClosedUntilBackfilledAndRecoversDeterministically(t *testing.T) {
	t.Run("fails closed before backfill and recovers on prepareCompletedStartup", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		upgraded := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(upgraded, fixture.runtime, fixture.destination.URL())

		if upgraded.gatewayAdmissionProven(ctx) {
			t.Fatal("historical completed adoption without journal proved Gateway admission before backfill")
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)
		cfgBefore := readGatewayConfigView(t, upgraded)
		if cfgBefore.Capability.Writable {
			t.Fatalf("historical adoption writable before backfill: %+v", cfgBefore.Capability)
		}
		if listed, err := upgraded.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("keys before backfill = %#v %v", listed, err)
		}

		upgraded.prepareCompletedStartup(ctx, preUpgrade.Operation)
		if got := fixture.reload(t).Operation.Revision; got != preUpgrade.Operation.Revision {
			t.Fatalf("startup backfill mutated revision %d -> %d", preUpgrade.Operation.Revision, got)
		}
		if !upgraded.gatewayAdmissionProven(ctx) {
			t.Fatal("backfilled historical adoption did not prove Gateway admission")
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, true)
		listed, err := upgraded.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys) {
			t.Fatalf("backfilled keys = %#v %v", listed, err)
		}
		var activeBindings int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null and runtime_identity='runtime'`).Scan(&activeBindings); err != nil || activeBindings != len(adoptedClientKeys) {
			t.Fatalf("backfilled source bindings = %d (%v), want %d", activeBindings, err, len(adoptedClientKeys))
		}

		cfgAfter := readGatewayConfigView(t, upgraded)
		if !cfgAfter.Capability.Writable {
			t.Fatalf("backfilled gateway capability = %+v", cfgAfter.Capability)
		}
		save := gatewayRequest(t, upgraded, http.MethodPost, "gateway/config/groups/routing",
			`{"requestId":"`+strings.Repeat("88", 16)+`","expected":{"generation":"`+cfgAfter.Generation+`","baseline":"`+groupBaseline(t, cfgAfter, "routing")+`"},"edits":[{"path":"routing.strategy","action":"set","value":"fill-first"}]}`,
			"application/json")
		if save.Code != http.StatusOK || fixture.destination.value("routing", "strategy") != "fill-first" {
			t.Fatalf("backfilled config save = %d %s", save.Code, save.Body.String())
		}
		replay := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if replay.Code != http.StatusOK {
			t.Fatalf("post-backfill complete replay = %d %s", replay.Code, replay.Body.String())
		}
	})

	t.Run("POST complete on historical unjournaled adoption refuses foreign journal with 409 instead of silent 200", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		if _, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.app.keyRepo.ClaimInitialInstall(ctx, freshkeys.InitialInstall{
			KeyID: strings.Repeat("c", 32), RunID: "managed:foreign-run", Generation: "3", AttemptedAtMS: 1,
		}); err != nil {
			t.Fatal(err)
		}

		resp := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if resp.Code != http.StatusConflict || errorCode(t, resp) != "adopt_installation_conflict" {
			t.Fatalf("conflicted historical complete = %d %s", resp.Code, resp.Body.String())
		}
		if fixture.app.gatewayAdmissionProven(ctx) {
			t.Fatal("conflicted historical adoption proved Gateway admission")
		}
		assertFreshAuthorityAdmission(t, fixture.app, loaded.Operation, false)
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("conflicted historical backfill left keys: %#v %v", listed, err)
		}
	})

	t.Run("POST initial-install on historical completed adoption returns 409 without journal or CPA write and backfill still re-proves", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		upgraded := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(upgraded, fixture.runtime, fixture.destination.URL())
		fixture.destination.mu.Lock()
		writesBefore := len(fixture.destination.writes)
		fixture.destination.mu.Unlock()

		createResp := gatewayRequest(t, upgraded, http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("a", 32)+`"}`, "application/json")
		if createResp.Code != http.StatusCreated {
			t.Fatalf("POST api-keys = %d %s", createResp.Code, createResp.Body.String())
		}
		var created struct {
			Key struct {
				ID string `json:"id"`
			} `json:"key"`
		}
		if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil || created.Key.ID == "" {
			t.Fatalf("decode created workspace key: %v %s", err, createResp.Body.String())
		}

		installResp := gatewayRequest(t, upgraded, http.MethodPost, "api-keys/"+created.Key.ID+"/initial-install", `{}`, "application/json")
		if installResp.Code != http.StatusConflict {
			t.Fatalf("POST initial-install on historical adopt = %d %s, want 409", installResp.Code, installResp.Body.String())
		}
		if _, applyErr := upgraded.keyApply.Apply(ctx, created.Key.ID, preUpgrade.Operation.Strategy.TargetGeneration); !errors.Is(applyErr, freshkeyssvc.ErrConflict) {
			t.Fatalf("service Apply on historical adopt = %v, want ErrConflict", applyErr)
		}
		if journal, jErr := upgraded.keyRepo.InitialInstall(ctx); jErr != nil || journal != nil {
			t.Fatalf("refused initial-install wrote journal = %#v (%v)", journal, jErr)
		}
		fixture.destination.mu.Lock()
		writesAfter := len(fixture.destination.writes)
		fixture.destination.mu.Unlock()
		if writesAfter != writesBefore {
			t.Fatalf("refused initial-install wrote to destination CPA: %d -> %d", writesBefore, writesAfter)
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)

		// Backfill must still run the read-only destination proof: while the
		// destination management read fails, neither startup nor POST complete
		// admits or treats the installation as ready.
		fixture.destination.mu.Lock()
		origAuthFn := fixture.destination.authFn
		fixture.destination.authFn = func() string { return "temporary-outage-key" }
		fixture.destination.mu.Unlock()
		upgraded.prepareCompletedStartup(ctx, preUpgrade.Operation)
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)
		unprovenReplay := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if unprovenReplay.Code != http.StatusServiceUnavailable || errorCode(t, unprovenReplay) != "adopt_destination_unproven" {
			t.Fatalf("POST complete while destination unproven = %d %s, want 503 adopt_destination_unproven", unprovenReplay.Code, unprovenReplay.Body.String())
		}

		fixture.destination.mu.Lock()
		fixture.destination.authFn = origAuthFn
		fixture.destination.mu.Unlock()
		provenReplay := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if provenReplay.Code != http.StatusOK {
			t.Fatalf("POST complete after destination recovery = %d %s", provenReplay.Code, provenReplay.Body.String())
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, true)
		listed, err := upgraded.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys)+1 {
			t.Fatalf("keys after backfill = %#v (%v), want %d", listed, err, len(adoptedClientKeys)+1)
		}
	})

	t.Run("CPA write failure or pre-existing non-adoption journal never admits Gateway and backfill still re-proves", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		upgraded := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(upgraded, fixture.runtime, fixture.destination.URL())
		fixture.destination.mu.Lock()
		fixture.destination.writeFailures["access/api-keys"] = http.StatusConflict
		fixture.destination.mu.Unlock()

		staged, _, err := upgraded.keys.Create(ctx, strings.Repeat("b", 32))
		if err != nil {
			t.Fatal(err)
		}
		installResp := gatewayRequest(t, upgraded, http.MethodPost, "api-keys/"+string(staged.Key.ID)+"/initial-install", `{}`, "application/json")
		if installResp.Code != http.StatusConflict {
			t.Fatalf("POST initial-install with failing CPA write = %d %s, want 409", installResp.Code, installResp.Body.String())
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)

		// Even if a pre-upgrade installation already persisted a field-matching
		// ClaimInitialInstall row when a CPA write failed, every consumer must
		// stay fail-closed and backfill must still perform its read-only re-proof.
		if err := upgraded.keyRepo.ClaimInitialInstall(ctx, freshkeys.InitialInstall{
			KeyID:              string(staged.Key.ID),
			RunID:              freshkeyapply.ManagedRunID(string(preUpgrade.Operation.Identity.OperationID)),
			Generation:         "3",
			AttemptedAtMS:      1,
			ProviderSeedSHA256: freshkeyapply.Permit{}.S2B.Binding(),
		}); err != nil {
			t.Fatal(err)
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)
		if cfgSuspicious := readGatewayConfigView(t, upgraded); cfgSuspicious.Capability.Writable {
			t.Fatalf("suspicious ClaimInitialInstall journal unlocked gateway writes: %+v", cfgSuspicious.Capability)
		}
		if ready, readyErr := upgraded.keyApply.AdoptionRegistrationReady(ctx); readyErr != nil || ready {
			t.Fatalf("AdoptionRegistrationReady on suspicious journal = (%v, %v), want (false, nil)", ready, readyErr)
		}

		// Destination read failure proves backfill did NOT short-circuit on the
		// suspicious journal.
		fixture.destination.mu.Lock()
		origAuthFn := fixture.destination.authFn
		fixture.destination.authFn = func() string { return "destination-down" }
		fixture.destination.mu.Unlock()
		if downResp := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json"); downResp.Code != http.StatusServiceUnavailable || errorCode(t, downResp) != "adopt_destination_unproven" {
			t.Fatalf("POST complete with suspicious journal while destination down = %d %s, want 503 adopt_destination_unproven", downResp.Code, downResp.Body.String())
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, false)

		// Once destination read-only proof succeeds, backfill upgrades the journal
		// with CommitAdoptionTx evidence and registers all adopted keys.
		fixture.destination.mu.Lock()
		fixture.destination.authFn = origAuthFn
		fixture.destination.mu.Unlock()
		if okResp := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json"); okResp.Code != http.StatusOK {
			t.Fatalf("POST complete recovery = %d %s", okResp.Code, okResp.Body.String())
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, true)
		listed, err := upgraded.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys)+1 {
			t.Fatalf("recovered keys = %#v (%v), want %d", listed, err, len(adoptedClientKeys)+1)
		}
	})

	t.Run("concurrent POST complete across two instances and repeated prepareCompletedStartup register adopted keys once", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		appA := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(appA, fixture.runtime, fixture.destination.URL())
		appB := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(appB, fixture.runtime, fixture.destination.URL())

		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := make([]int, 2)
		bodies := make([]string, 2)
		for idx, candidateApp := range []*freshApplication{appA, appB} {
			wg.Add(1)
			go func(slot int, target *freshApplication) {
				defer wg.Done()
				<-start
				rec := serveHTTPRequest(t, target, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
				codes[slot] = rec.Code
				bodies[slot] = rec.Body.String()
			}(idx, candidateApp)
		}
		close(start)
		wg.Wait()
		for idx, code := range codes {
			if code != http.StatusOK {
				t.Fatalf("concurrent POST complete[%d] = %d %s, want 200", idx, code, bodies[idx])
			}
		}
		for range 3 {
			appA.prepareCompletedStartup(ctx, preUpgrade.Operation)
			appB.prepareCompletedStartup(ctx, preUpgrade.Operation)
		}
		if gotRev := fixture.reload(t).Operation.Revision; gotRev != preUpgrade.Operation.Revision {
			t.Fatalf("concurrent/repeated backfill mutated revision %d -> %d", preUpgrade.Operation.Revision, gotRev)
		}
		listed, err := appA.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys) {
			t.Fatalf("keys after concurrent complete + repeated startup = %#v (%v), want %d", listed, err, len(adoptedClientKeys))
		}
		var activeBindings int
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null and runtime_identity='runtime'`).Scan(&activeBindings); err != nil || activeBindings != len(adoptedClientKeys) {
			t.Fatalf("active source bindings = %d (%v), want %d", activeBindings, err, len(adoptedClientKeys))
		}
		assertFreshAuthorityAdmission(t, appA, preUpgrade.Operation, true)
		assertFreshAuthorityAdmission(t, appB, preUpgrade.Operation, true)
	})

	t.Run("interleaved completion commit between VerifyAdoptionRegistration reads does not report 409 conflict and completes backfill", func(t *testing.T) {
		for _, preloadLegacyJournal := range []bool{false, true} {
			mode := "unjournaled"
			if preloadLegacyJournal {
				mode = "legacy_non_adoption_journal"
			}
			t.Run(mode, func(t *testing.T) {
				fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
				ctx := t.Context()
				loaded := fixture.reload(t)
				preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
					r.Operation.ProductComplete = true
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}

				appA := newFreshApplication(fixture.opened, config.Config{}, nil)
				wireAdoptManagedAuthority(appA, fixture.runtime, fixture.destination.URL())
				appB := newFreshApplication(fixture.opened, config.Config{}, nil)
				wireAdoptManagedAuthority(appB, fixture.runtime, fixture.destination.URL())

				wantKeyCount := len(adoptedClientKeys)
				if preloadLegacyJournal {
					staged, _, createErr := appA.keys.Create(ctx, strings.Repeat("c", 32))
					if createErr != nil {
						t.Fatal(createErr)
					}
					if claimErr := appA.keyRepo.ClaimInitialInstall(ctx, freshkeys.InitialInstall{
						KeyID:              string(staged.Key.ID),
						RunID:              freshkeyapply.ManagedRunID(string(preUpgrade.Operation.Identity.OperationID)),
						Generation:         "3",
						AttemptedAtMS:      1,
						ProviderSeedSHA256: freshkeyapply.Permit{}.S2B.Binding(),
					}); claimErr != nil {
						t.Fatal(claimErr)
					}
					wantKeyCount++
				}

				var interleaved sync.Once
				appA.keyRepo.SetVerifyAfterInitialInstallHookForTest(func() {
					interleaved.Do(func() {
						peerRec := serveHTTPRequest(t, appB, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
						if peerRec.Code != http.StatusOK {
							t.Errorf("interleaved peer POST complete = %d %s, want 200", peerRec.Code, peerRec.Body.String())
						}
					})
				})
				t.Cleanup(func() { appA.keyRepo.SetVerifyAfterInitialInstallHookForTest(nil) })

				replayRec := serveHTTPRequest(t, appA, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
				if replayRec.Code != http.StatusOK {
					t.Fatalf("interleaved POST complete (%s) = %d %s, want 200", mode, replayRec.Code, replayRec.Body.String())
				}
				appA.keyRepo.SetVerifyAfterInitialInstallHookForTest(nil)
				if completion := appA.reconcileCompletedAdoption(ctx); completion.status != 0 {
					t.Fatalf("reconcileCompletedAdoption after interleaved commit (%s) = %d %s", mode, completion.status, completion.code)
				}
				listed, err := appA.keys.List(ctx)
				if err != nil || len(listed) != wantKeyCount {
					t.Fatalf("keys after interleaved backfill (%s) = %#v (%v), want %d", mode, listed, err, wantKeyCount)
				}
				assertFreshAuthorityAdmission(t, appA, preUpgrade.Operation, true)
				assertFreshAuthorityAdmission(t, appB, preUpgrade.Operation, true)
			})
		}
	})
	t.Run("interleaved backfill across two instances observing different destination key snapshots rejects second snapshot with 409 and leaves no extra key", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		const extraSecret = "source-client-three"
		extraHash := sha256Hex(extraSecret)

		appA := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(appA, fixture.runtime, fixture.destination.URL())
		appB := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(appB, fixture.runtime, fixture.destination.URL())
		realStoreA := appA.store
		realStoreB := appB.store

		appAPrepared := make(chan struct{})
		allowAppACommit := make(chan struct{})
		appADone := make(chan struct{})
		closeOnceA := sync.OnceFunc(func() { close(appAPrepared) })
		closeAllowA := sync.OnceFunc(func() { close(allowAppACommit) })
		closeDoneA := sync.OnceFunc(func() { close(appADone) })
		t.Cleanup(func() {
			closeOnceA()
			closeAllowA()
			closeDoneA()
		})

		appA.store = &loadFailingStore{
			Store: realStoreA,
			beforeWithRecordTx: func() {
				closeOnceA()
				<-allowAppACommit
			},
		}
		var journalAfterAppA *freshkeys.InitialInstall
		appB.store = &loadFailingStore{
			Store: realStoreB,
			beforeWithRecordTx: func() {
				closeAllowA()
				<-appADone
				var jErr error
				journalAfterAppA, jErr = appA.keyRepo.InitialInstall(ctx)
				if jErr != nil || journalAfterAppA == nil || journalAfterAppA.AdoptedSetSHA256 == "" {
					t.Errorf("journal after appA commit = %#v (%v)", journalAfterAppA, jErr)
				}
			},
		}

		var (
			recA *httptest.ResponseRecorder
			recB *httptest.ResponseRecorder
			wg   sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer closeDoneA()
			recA = serveHTTPRequest(t, appA, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		}()

		// Wait until appA has finished its read-only destination proof of {A, B},
		// then advance destination CPA's key list to {A, B, C} before appB proves.
		<-appAPrepared
		fixture.destination.mu.Lock()
		fixture.destination.config["access"] = map[string]any{
			"api-keys": []any{adoptedClientKeys[0], adoptedClientKeys[1], extraSecret},
		}
		fixture.destination.mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			recB = serveHTTPRequest(t, appB, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		}()
		wg.Wait()

		if recA.Code != http.StatusOK {
			t.Fatalf("first snapshot POST complete = %d %s, want 200", recA.Code, recA.Body.String())
		}
		if recB.Code != http.StatusConflict || errorCode(t, recB) != "adopt_installation_conflict" {
			t.Fatalf("second divergent-snapshot POST complete = %d %s, want 409 adopt_installation_conflict", recB.Code, recB.Body.String())
		}
		afterJournal, err := appA.keyRepo.InitialInstall(ctx)
		if err != nil || afterJournal == nil || journalAfterAppA == nil || *afterJournal != *journalAfterAppA {
			t.Fatalf("committed journal mutated across divergent conflict: got %#v (%v), want %#v", afterJournal, err, journalAfterAppA)
		}

		listed, err := appA.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys) {
			t.Fatalf("workspace keys after divergent-snapshot conflict = %#v (%v), want %d", listed, err, len(adoptedClientKeys))
		}
		var keyCount, identityCount, bindingCount int
		if err := fixture.opened.DB.QueryRow(`select count(*) from fresh_client_api_keys where secret_hash=?`, extraHash).Scan(&keyCount); err != nil || keyCount != 0 {
			t.Fatalf("extra key staged in fresh_client_api_keys: count=%d err=%v", keyCount, err)
		}
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_identities`).Scan(&identityCount); err != nil || identityCount != len(adoptedClientKeys) {
			t.Fatalf("gateway_api_key_identities count = %d (%v), want %d", identityCount, err, len(adoptedClientKeys))
		}
		if err := fixture.opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_hash=?`, extraHash).Scan(&bindingCount); err != nil || bindingCount != 0 {
			t.Fatalf("extra key bound in gateway_api_key_source_bindings: count=%d err=%v", bindingCount, err)
		}
		assertFreshAuthorityAdmission(t, appA, preUpgrade.Operation, true)
		assertFreshAuthorityAdmission(t, appB, preUpgrade.Operation, true)
	})
}

func assertFreshAuthorityAdmission(t *testing.T, app *freshApplication, op bootstrap.FreshOperation, wantAdmitted bool) {
	t.Helper()
	authResp := httptest.NewRecorder()
	app.ServeHTTP(authResp, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority", nil))
	if authResp.Code != http.StatusOK {
		t.Fatalf("GET /internal/routing/fresh-authority = %d %s, want 200", authResp.Code, authResp.Body.String())
	}
	if err := validateFreshAuthorityBody(authResp.Body.Bytes(), op, wantAdmitted); err != nil {
		t.Fatalf("%v (body=%s)", err, authResp.Body.String())
	}
}

func validateFreshAuthorityBody(body []byte, op bootstrap.FreshOperation, wantAdmitted bool) error {
	type strictGatewayAdmission struct {
		InstallationID *string `json:"installationId"`
		Generation     *string `json:"generation"`
		Admitted       *bool   `json:"admitted"`
	}
	type strictFreshAuthorityReply struct {
		freshrouting.State
		GatewayAdmission *strictGatewayAdmission `json:"gatewayAdmission"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var reply strictFreshAuthorityReply
	if err := dec.Decode(&reply); err != nil {
		return fmt.Errorf("decode freshAuthorityReply: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing JSON in fresh-authority response: %v", err)
	}
	if reply.GatewayAdmission == nil {
		return errors.New("fresh-authority missing or null gatewayAdmission object")
	}
	if reply.GatewayAdmission.InstallationID == nil {
		return errors.New("gatewayAdmission missing or null installationId")
	}
	if reply.GatewayAdmission.Generation == nil {
		return errors.New("gatewayAdmission missing or null generation")
	}
	if reply.GatewayAdmission.Admitted == nil {
		return errors.New("gatewayAdmission missing or null admitted boolean")
	}
	var prodReply freshAuthorityReply
	if err := json.Unmarshal(body, &prodReply); err != nil {
		return fmt.Errorf("unmarshal production freshAuthorityReply: %w", err)
	}
	wantInstallID := string(op.Identity.InstallationID)
	wantGen := string(op.Identity.Generation)
	if wantInstallID == "" || wantGen == "" {
		return fmt.Errorf("expected operation identity to carry non-empty installationId/generation: %+v", op.Identity)
	}
	if reply.InstallationID != wantInstallID || reply.Generation != wantGen {
		return fmt.Errorf("freshAuthorityReply routing identity = (%q, %q), want (%q, %q)", reply.InstallationID, reply.Generation, wantInstallID, wantGen)
	}
	if *reply.GatewayAdmission.InstallationID != wantInstallID || *reply.GatewayAdmission.Generation != wantGen {
		return fmt.Errorf("gatewayAdmission identity = (%q, %q), want (%q, %q)", *reply.GatewayAdmission.InstallationID, *reply.GatewayAdmission.Generation, wantInstallID, wantGen)
	}
	if *reply.GatewayAdmission.Admitted != wantAdmitted {
		return fmt.Errorf("gatewayAdmission.Admitted = %v, want %v", *reply.GatewayAdmission.Admitted, wantAdmitted)
	}
	return nil
}

func TestValidateFreshAuthorityBodyRejectsNullAndMissingAdmissionFields(t *testing.T) {
	op := bootstrap.FreshOperation{
		Identity: bootstrap.FreshIdentity{
			InstallationID: "install-01",
			Generation:     "gen-01",
			OperationID:    "fresh-op-01",
		},
	}
	base := `{"installationId":"install-01","generation":"gen-01","operationId":"fresh-op-01","revision":1,"target":"/control","committed":true`
	for _, tc := range []struct {
		name         string
		body         string
		wantAdmitted bool
	}{
		{name: "missing gatewayAdmission", body: base + `}`, wantAdmitted: false},
		{name: "null gatewayAdmission", body: base + `,"gatewayAdmission":null}`, wantAdmitted: false},
		{name: "missing admitted boolean", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01"}}`, wantAdmitted: false},
		{name: "null admitted boolean when expecting false", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":null}}`, wantAdmitted: false},
		{name: "null admitted boolean when expecting true", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":null}}`, wantAdmitted: true},
		{name: "null installationId", body: base + `,"gatewayAdmission":{"installationId":null,"generation":"gen-01","admitted":false}}`, wantAdmitted: false},
		{name: "null generation", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":null,"admitted":false}}`, wantAdmitted: false},
		{name: "admitted mismatch false vs true", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":false}}`, wantAdmitted: true},
		{name: "admitted mismatch true vs false", body: base + `,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":true}}`, wantAdmitted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateFreshAuthorityBody([]byte(tc.body), op, tc.wantAdmitted); err == nil {
				t.Fatalf("validateFreshAuthorityBody(%s) succeeded unexpectedly for %s", tc.name, tc.body)
			}
		})
	}
	if err := validateFreshAuthorityBody([]byte(base+`,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":false}}`), op, false); err != nil {
		t.Fatalf("valid admitted=false rejected: %v", err)
	}
	if err := validateFreshAuthorityBody([]byte(base+`,"gatewayAdmission":{"installationId":"install-01","generation":"gen-01","admitted":true}}`), op, true); err != nil {
		t.Fatalf("valid admitted=true rejected: %v", err)
	}
}

// Production wiring builds `app.keyApply` via `NewStrategySelector` backed by
// `gatedEmbeddedRuntimeClient`: completion, historical backfill, direct
// `RegisterAdoption`, `store.Load` outage classification, and key revocation
// `Preflight` must all delegate through the active Managed authority without
// triggering any embedded Runtime lifecycle mutation.
func TestAdoptCompletionBackfillAndKeyPreflightWorkThroughStrategySelectorAndGatedRuntime(t *testing.T) {
	t.Run("completion and key revocation preflight delegate through StrategySelector", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()

		completeResp := serveHTTPRequest(t, fixture.app, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if completeResp.Code != http.StatusOK {
			t.Fatalf("POST complete via StrategySelector = %d %s, want 200", completeResp.Code, completeResp.Body.String())
		}
		listed, err := fixture.app.keys.List(ctx)
		if err != nil || len(listed) != len(adoptedClientKeys) {
			t.Fatalf("listed adopted keys = %#v (%v), want %d", listed, err, len(adoptedClientKeys))
		}
		// Idempotent RegisterAdoption through the StrategySelector wrapper.
		if replayed, regErr := fixture.app.keyApply.RegisterAdoption(ctx, adoptedClientKeys); regErr != nil || len(replayed) != len(adoptedClientKeys) {
			t.Fatalf("StrategySelector.RegisterAdoption replay = %#v (%v)", replayed, regErr)
		}

		preflightPath := "api-keys/" + string(listed[0].ID) + "/revocation/" + string(listed[1].ID) + "/revoke"
		preflightResp := gatewayRequest(t, fixture.app, http.MethodGet, preflightPath, "", "")
		if preflightResp.Code != http.StatusOK {
			t.Fatalf("GET %s via StrategySelector = %d %s, want 200", preflightPath, preflightResp.Code, preflightResp.Body.String())
		}
		var preflight struct {
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(preflightResp.Body.Bytes(), &preflight); err != nil || !preflight.Ready || preflight.Reason != "" {
			t.Fatalf("preflight verdict = %+v (%v), want ready=true", preflight, err)
		}
	})

	t.Run("historical backfill uses read-only gated runtime Status and preserves 503 on store outage", func(t *testing.T) {
		fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
		ctx := t.Context()
		loaded := fixture.reload(t)
		preUpgrade, err := fixture.app.store.Update(ctx, loaded.Operation.Identity, loaded.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		upgraded := newFreshApplication(fixture.opened, config.Config{}, nil)
		wireAdoptManagedAuthority(upgraded, fixture.runtime, fixture.destination.URL())
		realStore := upgraded.store
		upgraded.pluginCapability = nil
		startsBefore := len(fixture.runtime.starts)
		stagesBefore := len(fixture.runtime.stages)

		// Store read outage inside reconcileCompletedAdoption under StrategySelector
		// must return 503 adopt_installation_unavailable, never 409.
		upgraded.store = &loadFailingStore{
			Store:        realStore,
			failFromCall: 4,
			err:          errors.New("simulated sqlite read outage during replay"),
		}
		upgraded.routing = freshrouting.New(upgraded.store, activeAdoptRoutingDriver{store: upgraded.store})
		outageResp := serveHTTPRequest(t, upgraded, http.MethodPost, "/control/api/v2/complete", `{}`, "application/json")
		if outageResp.Code != http.StatusServiceUnavailable || errorCode(t, outageResp) != "adopt_installation_unavailable" {
			t.Fatalf("POST complete replay store outage = %d %s, want 503 adopt_installation_unavailable", outageResp.Code, outageResp.Body.String())
		}

		upgraded.store = realStore
		upgraded.routing = freshrouting.New(realStore, activeAdoptRoutingDriver{store: realStore})
		upgraded.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, upgraded.v8PluginCapable)
		upgraded.prepareCompletedStartup(ctx, preUpgrade.Operation)
		if len(fixture.runtime.starts) != startsBefore || len(fixture.runtime.stages) != stagesBefore {
			t.Fatalf("historical backfill mutated runtime lifecycle: starts %d->%d, stages %d->%d",
				startsBefore, len(fixture.runtime.starts), stagesBefore, len(fixture.runtime.stages))
		}
		assertFreshAuthorityAdmission(t, upgraded, preUpgrade.Operation, true)
	})
}
