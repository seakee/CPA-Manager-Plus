package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
)

func TestFreshUpdateChannelSQLiteAdminRestartAndFailure(t *testing.T) {
	ctx := t.Context()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if opened != nil {
			_ = opened.Close()
		}
	}()
	app := newFreshApplication(opened, config.Config{}, nil)
	_, err = app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+"settings/update-channel", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		httpapi.InitialFullWebHandler(currentInstallControlPrefix, http.HandlerFunc(app.canonicalAPI)).ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), strategyTestAdmin) {
			t.Fatal("cache or credential exposure")
		}
		return rec
	}
	value := func(want string) {
		t.Helper()
		got := call("GET", strategyTestAdmin, "")
		var v struct {
			Preference string `json:"channel_preference"`
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &v) != nil || v.Preference != want {
			t.Fatalf("read %s: %d %s", want, got.Code, got.Body.String())
		}
	}
	for _, token := range []string{"", "wrong-admin", "cpa-management-key", "cpamp_sk_" + strings.Repeat("a", 64)} {
		for _, method := range []string{"GET", "PUT"} {
			if call(method, token, `{"channel_preference":"stable"}`).Code != 401 {
				t.Fatal("unauthorized access")
			}
		}
	}
	value("auto")
	for _, invalid := range []string{`{}`, `null`, `{"channel_preference":"invalid"}`, `{"channel_preference":"STABLE"}`, `{"channel_preference":"stable","extra":true}`, `{"channel_preference":"stable"} {}`, strings.Repeat("x", 1025)} {
		if got := call("PUT", strategyTestAdmin, invalid); got.Code != 400 {
			t.Fatalf("invalid accepted: %d", got.Code)
		}
	}
	if got := call("POST", strategyTestAdmin, "{}"); got.Code != 405 {
		t.Fatal("wrong method")
	}
	value("auto")
	// Retain actual existing check metadata and all unrelated settings.
	seed := updatecheck.State{SchemaVersion: 1, Preference: "auto", LastAttempt: time.Now().UTC(), LastSuccess: time.Now().UTC(), LastError: "update source returned HTTP 502", Releases: map[string]updatecheck.ReleaseInfo{}}
	data, _ := json.Marshal(seed)
	if err := setting.SaveUpdateCheck(ctx, opened.DB, data); err != nil {
		t.Fatal(err)
	}
	readOthers := func() map[string]string {
		t.Helper()
		rows, err := opened.DB.Query("select key,value from settings where key != 'manager_update_check_v1'")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			out[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := readOthers()
	for _, channel := range []string{"stable", "rc", "beta", "auto", "stable"} {
		if got := call("PUT", strategyTestAdmin, `{"channel_preference":"`+channel+`"}`); got.Code != 200 {
			t.Fatalf("save: %d", got.Code)
		}
		value(channel)
	}
	data, err = setting.LoadUpdateCheck(ctx, opened.DB)
	if err != nil {
		t.Fatal(err)
	}
	var after updatecheck.State
	if json.Unmarshal(data, &after) != nil {
		t.Fatal("bad state")
	}
	after.Preference = seed.Preference
	if !reflect.DeepEqual(seed, after) || !reflect.DeepEqual(before, readOthers()) {
		t.Fatal("save overwrote other state")
	}
	// SQLite itself rejects the write, after the service has cached state.
	if _, err := opened.DB.Exec("create trigger fail_channel before insert on settings when NEW.key='manager_update_check_v1' begin select raise(abort,'synthetic disk failure'); end"); err != nil {
		t.Fatal(err)
	}
	if got := call("PUT", strategyTestAdmin, `{"channel_preference":"beta"}`); got.Code != 503 || strings.Contains(got.Body.String(), "disk failure") {
		t.Fatal("failed write acknowledged/leaked")
	}
	value("stable")
	if _, err := opened.DB.Exec("drop trigger fail_channel"); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	opened = nil
	opened, err = fresh.Open(ctx, paths, fresh.OpenOptions{AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	app = newFreshApplication(opened, config.Config{}, nil)
	value("stable")
	if got := call("PUT", strategyTestAdmin, `{"channel_preference":"auto"}`); got.Code != 200 {
		t.Fatal("restore failed")
	}
	value("auto")
	if !reflect.DeepEqual(before, readOthers()) {
		t.Fatal("restart changed authority")
	}
	record, _ := app.store.Load(ctx)
	for _, strategy := range []bootstrap.CPAStrategy{bootstrap.ConnectExternal, bootstrap.AdoptExisting} {
		op := record.Operation
		op.Strategy.Strategy = strategy
		rec := httptest.NewRecorder()
		app.updateChannel(rec, httptest.NewRequest("GET", "/", nil), op)
		if rec.Code != 409 {
			t.Fatal("non-New scope enabled")
		}
	}
}

func TestFreshCPAAdoptBaselineRequiresPanelAndCompletedSetup(t *testing.T) {
	prior := "sha256:" + strings.Repeat("a", 64)
	pin := "sha256:" + strings.Repeat("b", 64)
	var adopted atomic.Bool
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+artifactTestRuntimeToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		active, origin, version := prior, "acquired", "8.0.55"
		if adopted.Load() {
			active, origin, version = pin, "bundled", "8.0.56"
		}
		baseline := func(canAdopt bool) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": 1, "pinned": map[string]string{"artifactId": pin, "version": "8.0.56"},
				"active": map[string]string{"artifactId": active, "origin": origin}, "bundledAvailable": true, "adoptBaselineAvailable": canAdopt,
			})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/runtime/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion": "v1", "runtimeIdentity": "runtime-01", "runtimeGeneration": 1, "state": "ready", "cpaObservedVersion": version,
				"activeGatewayArtifact": map[string]string{"engine": "cpa", "artifactId": active, "version": version},
			})
		case "GET /v1/runtime/artifact":
			baseline(!adopted.Load())
		case "POST /v1/runtime/artifact/adopt-baseline":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["expectedActiveArtifactId"] != prior || !adopted.CompareAndSwap(false, true) {
				t.Errorf("unexpected adoption request: %v, %v", req, err)
				w.WriteHeader(http.StatusConflict)
				return
			}
			active, origin = pin, "bundled"
			baseline(false)
		default:
			http.NotFound(w, r)
		}
	}))
	defer supervisor.Close()
	app, _ := newArtifactTestSetup(t, supervisor.URL)
	commitArtifactTestRouting(t, app)
	installManagedCheckpoint(t, app, "committed")
	call := func(method, suffix, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		return doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), method, currentInstallManagementAPIV2Prefix+"cpamp/usage-service/runtime/updates"+suffix, token, "application/json", strings.NewReader(body), int64(len(body)))
	}
	admin := "Bearer " + strategyTestAdmin
	body := `{"expected_active_artifact_id":"` + prior + `"}`
	if res := call(http.MethodPost, "/adopt-baseline", "", body); res.Code != http.StatusUnauthorized || adopted.Load() {
		t.Fatalf("unauthenticated adoption = %d %s", res.Code, res.Body)
	}
	if res := call(http.MethodPost, "/adopt-baseline", admin, body); res.Code != http.StatusPreconditionRequired || adopted.Load() {
		t.Fatalf("incomplete adoption = %d %s", res.Code, res.Body)
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertStatus := func(expected string, canAdopt bool) {
		t.Helper()
		res := call(http.MethodGet, "", admin, "")
		var status cpaupdateservice.Status
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &status) != nil || status.ActiveArtifactID != expected || status.Baseline == nil || status.Baseline.PinnedArtifactID != pin || status.Baseline.AdoptBaselineAvailable != canAdopt {
			t.Fatalf("CPA baseline status = %d %s", res.Code, res.Body)
		}
	}
	assertStatus(prior, true)
	if res := call(http.MethodPost, "/adopt-baseline", admin, body); res.Code != http.StatusOK || !adopted.Load() {
		t.Fatalf("completed adoption = %d %s", res.Code, res.Body)
	}
	assertStatus(pin, false)
}
