package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
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
	source := managedInstallSource(app.store, app.protector, destinationURL)
	app.managedSource = source
	app.keyApply = freshkeyapply.NewManaged(app.keyRepo, app.keys, rt, destinationURL, source)
	app.gatewayConfig = freshconfig.New(app.store, app.keyApply.ConfigTarget,
		app.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
	app.gatewayConfig.EnableManaged(destinationURL)
	app.gatewayConfig.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		install, err := source(context.Background())
		if err != nil {
			return cpa.DestinationPosture{}, false
		}
		return cpa.PostureFor(install.Strategy)
	})
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
	failFromCall int32
	calls        atomic.Int32
	err          error
}

func (s *loadFailingStore) Load(ctx context.Context) (freshstore.Record, error) {
	if s.calls.Add(1) >= s.failFromCall {
		return freshstore.Record{}, s.err
	}
	return s.Store.Load(ctx)
}

func (s *loadFailingStore) UpdateTx(ctx context.Context, id bootstrap.FreshIdentity, rev uint64, fn func(*freshstore.Record, *sql.Tx) error) (freshstore.Record, error) {
	return s.Store.(freshstore.TransactionalUpdater).UpdateTx(ctx, id, rev, fn)
}

func (s *loadFailingStore) WithRecordTx(ctx context.Context, fn func(freshstore.Record, *sql.Tx) error) error {
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
//   1. before backfill, Gateway admission, config saves, plugin toggles and key
//      operations all fail closed consistently (no admission without a journal);
//   2. if backfill meets a foreign journal or an unproven destination, `POST
//      complete` refuses with 409/503 instead of returning a silent 200; and
//   3. `prepareCompletedStartup` (on Manager restart) or `POST complete`
//      atomically backfills the adopted keys, source bindings and journal once
//      without bumping `Operation.Revision`, restoring full authority.
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
		authResp := httptest.NewRecorder()
		upgraded.ServeHTTP(authResp, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority", nil))
		if authResp.Code != http.StatusOK || strings.Contains(authResp.Body.String(), `"admitted":true`) {
			t.Fatalf("fresh-authority before backfill = %d %s", authResp.Code, authResp.Body.String())
		}
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
		if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
			t.Fatalf("conflicted historical backfill left keys: %#v %v", listed, err)
		}
	})
}
