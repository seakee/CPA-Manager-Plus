package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func newA3TestApp(t *testing.T) (*freshApplication, func()) {
	t.Helper()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{Protector: opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}})
	op := opened.Record.Operation
	if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = op.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return app, func() { _ = opened.Close() }
}

func a3Post(t *testing.T, app *freshApplication, revision uint64, url, key string) *httptest.ResponseRecorder {
	return a3PostWithAttempt(t, app, revision, url, key, strings.Repeat("a", 32))
}

func a3PostWithAttempt(t *testing.T, app *freshApplication, revision uint64, url, key, attemptID string) *httptest.ResponseRecorder {
	t.Helper()
	op, err := app.current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		AttemptID        string                  `json:"attemptId"`
		freshstrategy.Request
	}{op.Identity, revision, attemptID, freshstrategy.Request{Strategy: bootstrap.ConnectExternal, BaseURL: url, ManagementKey: key}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	return response
}

func TestExternalOptionalPrivatePermitAndAtomicCompletion(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config" {
			t.Errorf("unexpected CPA source access")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ := app.current(t.Context())
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return false }
	denied := a3Post(t, app, op.Revision, source.URL, "private-source-secret")
	if denied.Code != http.StatusServiceUnavailable || sourceCalls.Load() != 0 {
		t.Fatal("untrusted Host accessed CPA", denied.Code)
	}
	still, _ := app.current(t.Context())
	if still.Revision != op.Revision || still.Strategy.Strategy != "" {
		t.Fatal("denied path persisted a checkpoint")
	}
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return true }
	accepted := a3Post(t, app, op.Revision, source.URL, "private-source-secret")
	if accepted.Code != http.StatusOK {
		t.Fatalf("A3 safe connection: %d %s", accepted.Code, accepted.Body.String())
	}
	committed, _ := app.current(t.Context())
	if committed.Revision != op.Revision+1 || committed.Strategy.Strategy != bootstrap.ConnectExternal ||
		committed.Strategy.Stage != "committed" || !committed.GatewayPending || committed.ProductComplete {
		t.Fatal("A3 response failed checkpoint contract", committed)
	}
	record, err := app.store.Load(t.Context())
	if err != nil || record.Connection == nil || record.Connection.ManagementKey == "private-source-secret" {
		t.Fatal("A3 secret was not protected", err)
	}
	if sourceCalls.Load() != 1 {
		t.Fatal("incorrect CPA safe-read count")
	}
	replay := a3Post(t, app, committed.Revision, source.URL, "private-source-secret")
	if replay.Code != http.StatusConflict || sourceCalls.Load() != 1 {
		t.Fatal("committed A3 replay accessed CPA", replay.Code)
	}
	complete := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"complete", nil)
	complete.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, complete)
	if response.Code != http.StatusOK {
		t.Fatal("External setup could not complete", response.Code)
	}
	after, _ := app.current(t.Context())
	if after.Revision != committed.Revision+1 || !after.ProductComplete || !after.GatewayPending {
		t.Fatal("External completion lost its boundary")
	}
	repeated := httptest.NewRecorder()
	app.canonicalAPI(repeated, complete)
	stillComplete, _ := app.current(t.Context())
	if repeated.Code != http.StatusOK || stillComplete.Revision != after.Revision {
		t.Fatal("completion was not idempotent")
	}
	if strings.Contains(response.Body.String(), "private-source-secret") {
		t.Fatal("completion leaked source key")
	}
}

func TestExternalFailedReadCanRetryWithoutDurableLock(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ := app.current(t.Context())
	result := a3Post(t, app, op.Revision, source.URL, "invalid-key")
	if result.Code == http.StatusOK {
		t.Fatal("source failure incorrectly saved")
	}
	current, _ := app.current(t.Context())
	if current.Strategy.Strategy != "" || current.Revision != op.Revision {
		t.Fatal("failed safe read locked setup", current)
	}
	result = a3Post(t, app, current.Revision, source.URL, "correct-key")
	if result.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatal("corrected connection failed", result.Code)
	}
	record, err := app.store.Load(t.Context())
	if err != nil || record.Connection == nil {
		t.Fatal("retry not persisted", err)
	}
	key, err := app.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil || key != "correct-key" {
		t.Fatal("incorrect retry key stored", err)
	}
}

func TestA3CASPreventsCompetingFirstPosts(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return true }
	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ := app.current(t.Context())
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = a3Post(t, app, op.Revision, source.URL, strings.Repeat("k", 24)).Code
		}(i)
	}
	wg.Wait()
	if !((results[0] == 200 && results[1] == 409) || (results[1] == 200 && results[0] == 409)) || reads.Load() < 1 || reads.Load() > 2 {
		t.Fatal("competing writes bypassed final CAS", results, reads.Load())
	}
}

func TestA3PermitBoundToOneInstallation(t *testing.T) {
	now := time.Now()
	op := bootstrap.FreshOperation{Identity: bootstrap.FreshIdentity{AdmissionID: "admit", InstallationID: "install", Generation: "gen", DatabasePath: "/data/manager.sqlite", OperationID: "operation-123456789"}}
	p := freshExternalPermit{Scope: freshExternalPrivateProfile, RunID: "fresh-private-run-123",
		AdmissionID: op.Identity.AdmissionID, InstallationID: op.Identity.InstallationID, Generation: op.Identity.Generation,
		DatabasePath: op.Identity.DatabasePath, OperationID: string(op.Identity.OperationID),
		IssuedAtMS: now.Add(-time.Second).UnixMilli(), ExpiresAtMS: now.Add(time.Minute).UnixMilli(),
		ExpectedRevision: 3, IntentDigest: freshExternalIntentDigest("fresh-private-run-123", 3, "http://cpa.private:8317", "private-source-secret", strings.Repeat("a", 32))}
	if !p.matchesIntent(op, 3, "http://cpa.private:8317", "private-source-secret", strings.Repeat("a", 32), now) {
		t.Fatal("valid bound private grant denied")
	}
	if p.matchesIntent(op, 3, "http://cpa.private:8317", "private-source-secret", strings.Repeat("b", 32), now) ||
		p.matchesIntent(op, 3, "http://cpa.private:8317", "private-source-secret", "bad", now) ||
		p.matchesIntent(op, 3, "http://foreign.private:8317", "private-source-secret", strings.Repeat("a", 32), now) ||
		p.matchesIntent(op, 3, "http://cpa.private:8317", "different-key", strings.Repeat("a", 32), now) ||
		p.matchesIntent(op, 4, "http://cpa.private:8317", "private-source-secret", strings.Repeat("a", 32), now) {
		t.Fatal("Host permit admitted a different original intent")
	}
	other := op
	other.Identity.Generation = "rotated"
	if p.valid(other, now) {
		t.Fatal("foreign generation admitted")
	}
	p.ExpiresAtMS = now.Add(-time.Second).UnixMilli()
	if p.valid(op, now) {
		t.Fatal("expired permit admitted")
	}
	p.ExpiresAtMS = now.Add(time.Minute).UnixMilli()
	p.Scope = "private-s1"
	if p.valid(op, now) {
		t.Fatal("S1 permit was accepted as External")
	}
}

// A preflight refusal must leave the entire durable Fresh record untouched.
// In particular a malformed Management Key cannot occupy a one-shot A3 slot.
func TestA3StaticPreflightDeniedWithoutReservation(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return true }
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ url, key string }{
		{"http://cpa.private:8317", ""},
		{"http://cpa.private:8317", "invalid key with space"},
		{"http://cpa.private:8317", "bad\nkey"},
		{"http://cpa.private:8317/%25", "private-source-secret"},
		{"http://cpa.private:0", "private-source-secret"},
		{"http://cpa.private:65536", "private-source-secret"},
		{"http://[::1]:65536", "private-source-secret"},
		{"http://cpa.private:", "private-source-secret"},
	} {
		got := a3Post(t, app, before.Operation.Revision, item.url, item.key)
		if got.Code != http.StatusConflict {
			t.Fatalf("preflight %q accepted: %d", item.url, got.Code)
		}
		after, err := app.store.Load(t.Context())
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("preflight persisted an attempt", err)
		}
	}
	app.strategy = freshstrategy.New(freshstrategy.Options{Protector: nil})
	got := a3Post(t, app, before.Operation.Revision, "http://cpa.private:8317", "private-source-secret")
	if got.Code != http.StatusConflict {
		t.Fatal("unqualified consumer reserved A3", got.Code)
	}
	after, err := app.store.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("unqualified path mutated record", err)
	}
}

// The original Host-owned permit binds the exact request, independent of the
// browser and network arrival order. A delayed first POST can still claim the
// slot; a fresh browser's different key or source must be refused before CAS.
func TestA3HostOriginalIntentRejectsReplacement(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ := app.current(t.Context())
	originalKey := "original-private-key"
	digest := freshExternalIntentDigest("private-host-run-123", op.Revision, source.URL, originalKey, strings.Repeat("a", 32))
	app.externalPermit = func(current bootstrap.FreshOperation, revision uint64, baseURL, key, attemptID string) bool {
		return current.Identity == op.Identity && revision == op.Revision &&
			freshExternalIntentDigest("private-host-run-123", revision, baseURL, key, attemptID) == digest
	}
	for _, altered := range []struct{ url, key, attemptID string }{
		{source.URL, "replacement-key", strings.Repeat("a", 32)},
		{"http://127.0.0.1:9", originalKey, strings.Repeat("a", 32)},
		{source.URL, originalKey, strings.Repeat("b", 32)},
	} {
		response := a3PostWithAttempt(t, app, op.Revision, altered.url, altered.key, altered.attemptID)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatal("replacement intent admitted", response.Code)
		}
		still, _ := app.current(t.Context())
		if still.Revision != op.Revision || still.Strategy.Strategy != "" || reads.Load() != 0 {
			t.Fatal("replacement intent consumed the original grant")
		}
	}
	for _, invalidID := range []string{"", "short", strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		response := a3PostWithAttempt(t, app, op.Revision, source.URL, originalKey, invalidID)
		if response.Code != http.StatusBadRequest || reads.Load() != 0 {
			t.Fatal("invalid attempt ID consumed Host grant", response.Code)
		}
	}
	accepted := a3Post(t, app, op.Revision, source.URL, originalKey)
	if accepted.Code != http.StatusOK || reads.Load() != 1 {
		t.Fatal("original request refused", accepted.Code)
	}
}

func TestPrivateExternalPermitRevocationBeforeCommit(t *testing.T) {
	for _, revokeAt := range []int{2, 3} {
		t.Run(strconv.Itoa(revokeAt), func(t *testing.T) {
			app, closeApp := newA3TestApp(t)
			defer closeApp()
			var checks atomic.Int32
			app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool {
				return checks.Add(1) < int32(revokeAt)
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			}))
			defer source.Close()
			op, _ := app.current(t.Context())
			response := a3Post(t, app, op.Revision, source.URL, "private-key")
			record, err := app.store.Load(t.Context())
			if response.Code != http.StatusServiceUnavailable || err != nil || record.Connection != nil || record.Operation.Revision != op.Revision {
				t.Fatal("revoked private grant saved a connection", response.Code, err)
			}
		})
	}
}

func TestExternalRecoversOldValidatedStateAndCompletedSQLiteRestart(t *testing.T) {
	ctx := t.Context()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	app := newFreshApplication(opened, config.Config{}, nil)
	op := opened.Record.Operation
	_, err = app.store.Update(ctx, op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = op.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, TargetIdentity: "external:sha256:" + strings.Repeat("a", 64), TargetGeneration: 1, Stage: "validated"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err = fresh.Open(ctx, paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	app = newFreshApplication(opened, config.Config{}, nil) // Real production consumer, no private permit.
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config" {
			t.Error("unexpected CPA mutation")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ = app.current(ctx)
	response := a3Post(t, app, op.Revision, source.URL, "external-key")
	if response.Code != http.StatusOK {
		t.Fatal("restart recovery failed", response.Code, response.Body.String())
	}
	complete := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"complete", nil)
	complete.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response = httptest.NewRecorder()
	app.canonicalAPI(response, complete)
	if response.Code != http.StatusOK {
		t.Fatal("completion failed", response.Code)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err = fresh.Open(ctx, paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app = newFreshApplication(opened, config.Config{}, nil)
	record, err := app.store.Load(ctx)
	if err != nil || !record.Operation.ProductComplete || !record.Operation.GatewayPending || record.Connection == nil {
		t.Fatal("completed setup lost after restart", err)
	}
	key, err := app.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil || key != "external-key" {
		t.Fatal("encrypted connection did not survive restart", err)
	}
	status := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/status", nil)
	status.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response = httptest.NewRecorder()
	app.canonicalAPI(response, status)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "external-key") {
		t.Fatal("existing Console inaccessible or leaked key", response.Code)
	}
}

func TestA3FixedBootstrapAdmissionMatchesCanonical(t *testing.T) {
	// The fixed session is intentionally retired at Routing.Committed. Verify
	// A3 before that transition; canonical admission is covered separately.
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{Protector: opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}})
	handoff, err := app.authority.Issue(t.Context())
	if err != nil || !handoff.Resume {
		t.Fatal("fixed session handoff unavailable", err)
	}
	encodedSession, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(encodedSession, &session); err != nil || session.Token == "" {
		t.Fatal("fixed session token invalid", err)
	}
	var sourceReads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceReads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	op, _ := app.current(t.Context())
	body, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		AttemptID        string                  `json:"attemptId"`
		freshstrategy.Request
	}{op.Identity, op.Revision, strings.Repeat("a", 32), freshstrategy.Request{Strategy: bootstrap.ConnectExternal, BaseURL: source.URL, ManagementKey: "private-key"}})
	if err != nil {
		t.Fatal(err)
	}
	perform := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", bytes.NewReader(body))
		request.Header.Set("Authorization", "Fresh "+session.Token)
		response := httptest.NewRecorder()
		app.fixedStrategy(response, request)
		return response
	}
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return false }
	denied := perform()
	if denied.Code != http.StatusServiceUnavailable || sourceReads.Load() != 0 {
		t.Fatal("fixed entry bypassed Host denial", denied.Code)
	}
	before, _ := app.current(t.Context())
	if before.Revision != op.Revision || before.Strategy.Strategy != "" {
		t.Fatal("fixed denial occupied A3 attempt")
	}
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return true }
	accepted := perform()
	if accepted.Code != http.StatusOK || sourceReads.Load() != 1 {
		t.Fatal("fixed entry did not complete a bounded accepted read", accepted.Code, accepted.Body.String())
	}
	current, _ := app.current(t.Context())
	if current.Revision != op.Revision+1 || current.Strategy.Stage != "committed" || current.ProductComplete || !current.GatewayPending {
		t.Fatal("fixed entry violated checkpoint shape", current)
	}
}

func TestExternalRequiresAdminBeforeCPAReadOrCompletion(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1) }))
	defer source.Close()
	op, _ := app.current(t.Context())
	for _, resource := range []string{"strategy", "complete"} {
		body, _ := json.Marshal(map[string]any{"identity": op.Identity, "expectedRevision": op.Revision, "attemptId": strings.Repeat("a", 32), "strategy": "connect_external", "baseUrl": source.URL, "managementKey": "test-secret"})
		request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewReader(body))
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		if response.Code != http.StatusUnauthorized || reads.Load() != 0 {
			t.Fatal("unauthorized request escaped boundary", response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"complete", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("unconfigured setup completed")
	}
}
