package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
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
