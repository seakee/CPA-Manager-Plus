package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityprojection"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

type freshUsageHarness struct {
	app             *freshApplication
	db              *sql.DB
	key             freshkeys.Key
	secret          string
	hash            string
	runtimeIdentity string
	// nowMS is a deterministic read instant after the key's creation so the
	// window, the collection start and the recorded calls are all comparable.
	nowMS   int64
	call    func(method, resource, body string) *httptest.ResponseRecorder
	cleanup func()
}

func newFreshUsageHarness(t *testing.T) *freshUsageHarness {
	t.Helper()
	ctx := t.Context()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	app := newFreshApplication(opened, config.Config{}, nil)
	if _, err = app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	markFreshProductComplete(t, app)
	harness := &freshUsageHarness{
		app: app, db: opened.DB, runtimeIdentity: string(validInstallationRuntime().Identity),
		cleanup: func() { _ = opened.Close() },
	}
	harness.call = func(method, resource, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		httpapi.InitialFullWebHandler("/control", http.HandlerFunc(app.canonicalAPI)).ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("usage read weakened API cache control")
		}
		payload := response.Body.String()
		if strings.Contains(payload, strategyTestAdmin) {
			t.Fatalf("%s leaked the admin credential: %s", resource, payload)
		}
		if strings.Contains(resource, "usage") {
			for _, forbidden := range []string{harness.secret, harness.hash, harness.runtimeIdentity} {
				if forbidden != "" && strings.Contains(payload, forbidden) {
					t.Fatalf("%s leaked key or runtime material: %s", resource, payload)
				}
			}
			if lowered := strings.ToLower(payload); strings.Contains(lowered, "managementkey") || strings.Contains(lowered, "secret") || strings.Contains(lowered, "hash") {
				t.Fatalf("%s leaked credential fields: %s", resource, payload)
			}
		}
		return response
	}

	created := harness.call("POST", "api-keys", `{"requestId":"`+strings.Repeat("a", 32)+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", created.Code, created.Body.String())
	}
	var result freshkeys.Result
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.CreatedSecret == "" {
		t.Fatalf("creation result: %v", err)
	}
	harness.key, harness.secret = result.Key, result.CreatedSecret
	sum := sha256.Sum256([]byte(result.CreatedSecret))
	harness.hash = hex.EncodeToString(sum[:])
	harness.nowMS = result.Key.CreatedAtMS + 3_600_000
	harness.markCollectionStartAt(t, result.Key.CreatedAtMS)
	harness.useCollection(t, usageapp.PhaseStopped)
	return harness
}

// markCollectionStartAt records a durable collection instant with its own
// clock, so the harness can prove collection from before the read window.
func (h *freshUsageHarness) markCollectionStartAt(t *testing.T, atMS int64) {
	t.Helper()
	service := usageapp.New(usageadapter.New(h.db), func() usageapp.CollectionPhase { return usageapp.PhaseStopped })
	service.SetClock(func() int64 { return atMS })
	if err := service.MarkCollectionStart(t.Context()); err != nil {
		t.Fatalf("mark collection start: %v", err)
	}
}

// useCollection keeps the real adapter and swaps only the live Collector phase.
func (h *freshUsageHarness) useCollection(t *testing.T, phase usageapp.CollectionPhase) {
	t.Helper()
	service := usageapp.New(usageadapter.New(h.db), func() usageapp.CollectionPhase { return phase })
	service.SetClock(func() int64 { return h.nowMS })
	h.app.usage = service
}

// markProjectionReady simulates the projection worker finishing a run after a
// binding change: the G1 revision trigger re-opens the checkpoint as pending.
func (h *freshUsageHarness) markProjectionReady(t *testing.T) {
	t.Helper()
	if _, err := h.db.Exec(`update gateway_usage_identity_projection_state
		set status = 'ready',
			last_processed_event_id = (select coalesce(max(id), 0) from usage_events),
			target_event_id = (select coalesce(max(id), 0) from usage_events)
		where state_name = 'canonical_identity_v1'`); err != nil {
		t.Fatalf("mark projection ready: %v", err)
	}
}

func (h *freshUsageHarness) bindingCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_id = ?`, string(h.key.ID)).Scan(&count); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	return count
}

func (h *freshUsageHarness) dropBinding(t *testing.T) {
	t.Helper()
	if _, err := h.db.Exec(`delete from gateway_api_key_source_bindings where api_key_id = ?`, string(h.key.ID)); err != nil {
		t.Fatalf("drop binding: %v", err)
	}
}

func (h *freshUsageHarness) insertUsage(t *testing.T, calls int) {
	t.Helper()
	for i := range calls {
		eventID := int64(i + 1)
		eventHash := sha256Hex(fmt.Sprintf("usage-%d", eventID))
		failed := 0
		if i%3 == 2 {
			failed = 1
		}
		timestampMS := h.key.CreatedAtMS + int64(i+1)*1000
		if _, err := h.db.Exec(`insert into usage_events
			(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
			values (?, ?, ?, ?, '2026-01-01T00:00:00Z', 'gpt-4', ?, 10, 5, 15, ?, ?)`,
			eventID, eventHash, fmt.Sprintf("request-%d", eventID), timestampMS, h.hash, failed, timestampMS); err != nil {
			t.Fatalf("insert usage event: %v", err)
		}
		if _, err := h.db.Exec(`insert into gateway_usage_identity_projection_v1
			(usage_event_id, event_hash, request_id, evidence_timestamp_ms, api_key_state, api_key_id, api_key_source_hash,
			 credential_state, credential_id, credential_source_auth_id, schema_version, projected_at_ms)
			values (?, ?, ?, ?, 'mapped', ?, ?, 'unknown', null, '', 1, ?)`,
			eventID, eventHash, fmt.Sprintf("request-%d", eventID), timestampMS, string(h.key.ID), h.hash, timestampMS); err != nil {
			t.Fatalf("insert projection: %v", err)
		}
	}
	if _, err := h.db.Exec(`update gateway_usage_identity_projection_state
		set status = 'ready', last_processed_event_id = ?, target_event_id = ? where state_name = 'canonical_identity_v1'`,
		calls, calls); err != nil {
		t.Fatalf("mark projection ready: %v", err)
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func decodeUsage[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	return value
}

func TestFreshKeyUsageRequiresACompletedInstall(t *testing.T) {
	harness := newFreshUsageHarness(t)
	defer harness.cleanup()
	detailPath := "api-keys/" + string(harness.key.ID) + "/usage"

	record, err := harness.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"api-keys/usage-summary", detailPath} {
		if got := harness.call("GET", resource, ""); got.Code != http.StatusConflict || got.Body.Len() != 0 {
			t.Fatalf("%s before completion = %d %s", resource, got.Code, got.Body.String())
		}
	}

	// External keeps the existing conflict semantics for usage as well.
	record, err = harness.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	external := record.Operation
	external.Strategy.Strategy = bootstrap.ConnectExternal
	for _, resource := range []string{"api-keys/usage-summary", detailPath} {
		response := httptest.NewRecorder()
		harness.app.clientKeys(response, httptest.NewRequest("GET", "/control/api/v2/"+resource, nil), external, resource)
		if response.Code != http.StatusConflict || response.Body.Len() != 0 {
			t.Fatalf("external %s = %d", resource, response.Code)
		}
	}

	markFreshProductComplete(t, harness.app)
	if got := harness.call("GET", "api-keys/usage-summary", ""); got.Code != http.StatusOK {
		t.Fatalf("completed install usage = %d %s", got.Code, got.Body.String())
	}
}

func TestFreshKeyUsageReportsRealNumbersAndHonestCoverage(t *testing.T) {
	harness := newFreshUsageHarness(t)
	defer harness.cleanup()
	summaryPath := "api-keys/usage-summary"
	detailPath := "api-keys/" + string(harness.key.ID) + "/usage"

	// Creation binds the key's source in the same transaction, so the
	// workspace has an attribution window from the first event on.
	if harness.bindingCount(t) != 1 {
		t.Fatal("created key has no source binding")
	}
	var firstSeen int64
	if err := harness.db.QueryRow(`select first_seen_at_ms from gateway_api_key_source_bindings where api_key_id = ?`,
		string(harness.key.ID)).Scan(&firstSeen); err != nil || firstSeen != harness.key.CreatedAtMS {
		t.Fatalf("binding window = %d err = %v, want the creation instant", firstSeen, err)
	}

	harness.insertUsage(t, 6)

	// The Collector is not running in this harness: Manager must not present a
	// quiet window it cannot prove.
	detail := decodeUsage[usageapp.DetailResponse](t, harness.call("GET", detailPath, ""))
	if detail.Coverage.State != "unavailable" || detail.Coverage.Reason != "collection_stopped" {
		t.Fatalf("stopped coverage = %+v", detail.Coverage)
	}
	if detail.Totals != nil || len(detail.Series) != 0 {
		t.Fatalf("stopped window published totals=%+v series=%+v", detail.Totals, detail.Series)
	}

	harness.useCollection(t, usageapp.PhaseRunning)
	// The default 24h window starts before the key was created, so its numbers
	// are a lower bound and the reported window opens at the key's own start.
	detail = decodeUsage[usageapp.DetailResponse](t, harness.call("GET", detailPath, ""))
	if detail.Coverage.State != "partial" || detail.Coverage.Reason != "window_partial" {
		t.Fatalf("default window coverage = %+v", detail.Coverage)
	}
	if detail.Coverage.CollectingSinceMS != harness.key.CreatedAtMS || detail.FromMS != harness.key.CreatedAtMS {
		t.Fatalf("coverage window = %+v from = %d", detail.Coverage, detail.FromMS)
	}
	if detail.Totals == nil || detail.Totals.Calls != 6 || detail.Totals.Failure != 2 || detail.Totals.Success != 4 ||
		detail.Totals.TotalTokens != 90 || detail.Totals.LastUsedAtMS == nil {
		t.Fatalf("detail totals = %+v", detail.Totals)
	}
	if len(detail.Series) != 1 || detail.Series[0].Calls != 6 {
		t.Fatalf("detail series = %+v", detail.Series)
	}

	// A window that starts where attribution started is fully provable.
	exactPath := fmt.Sprintf("%s?from_ms=%d&to_ms=%d", detailPath, harness.key.CreatedAtMS, harness.nowMS)
	exact := decodeUsage[usageapp.DetailResponse](t, harness.call("GET", exactPath, ""))
	if exact.Coverage.State != "complete" || exact.Coverage.Reason != "observed" {
		t.Fatalf("exact coverage = %+v", exact.Coverage)
	}
	if exact.FromMS != harness.key.CreatedAtMS || exact.Totals == nil || exact.Totals.Calls != 6 {
		t.Fatalf("exact window = %+v totals = %+v", exact, exact.Totals)
	}

	summary := decodeUsage[usageapp.SummaryResponse](t, harness.call("GET", summaryPath, ""))
	if summary.Coverage.State != "partial" || summary.Coverage.Reason != "window_partial" ||
		summary.Items[0].Calls != 6 || summary.Items[0].Coverage.Reason != "window_partial" ||
		summary.Items[0].LastUsedAtMS == nil {
		t.Fatalf("summary = %+v", summary)
	}

	// A key without an attribution window is unavailable, never zero.
	harness.dropBinding(t)
	summary = decodeUsage[usageapp.SummaryResponse](t, harness.call("GET", summaryPath, ""))
	if summary.Items[0].Coverage.State != "unavailable" || summary.Items[0].Coverage.Reason != "binding_missing" || summary.Items[0].Calls != 0 {
		t.Fatalf("unbound summary = %+v", summary)
	}
	detail = decodeUsage[usageapp.DetailResponse](t, harness.call("GET", detailPath, ""))
	if detail.Coverage.Reason != "binding_missing" || detail.Totals != nil {
		t.Fatalf("unbound detail = %+v", detail)
	}

	// The startup backfill re-establishes attribution from the key's creation,
	// so history recorded before the binding existed is not lost.
	bound, err := harness.app.keyRepo.BindFreshKeySources(t.Context(), harness.runtimeIdentity, harness.nowMS)
	if err != nil || bound != 1 {
		t.Fatalf("backfill = %d err = %v", bound, err)
	}
	if again, err := harness.app.keyRepo.BindFreshKeySources(t.Context(), harness.runtimeIdentity, harness.nowMS); err != nil || again != 0 {
		t.Fatalf("repeat backfill = %d err = %v", again, err)
	}
	// The new binding invalidates the ready checkpoint; the projection worker
	// re-runs and proves the window again.
	harness.markProjectionReady(t)
	summary = decodeUsage[usageapp.SummaryResponse](t, harness.call("GET", summaryPath, ""))
	if summary.Items[0].Coverage.State != "partial" || summary.Items[0].Calls != 6 ||
		summary.Coverage.State != "partial" {
		t.Fatalf("backfilled summary = %+v", summary)
	}

	// Revoked history stays readable: retirement closes attribution without
	// deleting what was already attributed.
	if _, err := harness.db.Exec(`update gateway_api_key_identities set lifecycle = 'superseded', revision = revision + 1 where id = ?`, string(harness.key.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.db.Exec(`update gateway_api_key_source_bindings set retired_at_ms = ? where api_key_id = ?`, harness.nowMS, string(harness.key.ID)); err != nil {
		t.Fatal(err)
	}
	harness.markProjectionReady(t)
	retained := decodeUsage[usageapp.DetailResponse](t, harness.call("GET", exactPath, ""))
	if retained.Totals == nil || retained.Totals.Calls != 6 || retained.Coverage.State != "complete" {
		t.Fatalf("revoked history = %+v", retained)
	}

	// Bad input stays a client error and an unknown key stays absent.
	if got := harness.call("GET", detailPath+"?from_ms=1", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("partial window accepted: %d", got.Code)
	}
	if got := harness.call("GET", detailPath+"?from_ms=100&to_ms=200&granularity=month", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown window parameter accepted: %d", got.Code)
	}
	if got := harness.call("GET", "api-keys/"+strings.Repeat("b", 32)+"/usage", ""); got.Code != http.StatusNotFound {
		t.Fatalf("unknown key usage: %d", got.Code)
	}
	if got := harness.call("GET", "api-keys/usage-summary", ""); got.Code != http.StatusOK {
		t.Fatalf("summary after revocation = %d", got.Code)
	}
}

// A ProductComplete reached inside the running process must arm the same usage
// chain as a resumed install: it backfills keys staged before completion and
// starts the Collector, projection and identity worker exactly once.
func TestFreshCompleteStartsUsageOnceAndBackfillsStagedKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	app := newFreshApplication(opened, config.Config{}, nil)
	app.runCtx = ctx
	quietProjection(app)
	if _, err := app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Stage the key before the installation commits: it has no attribution yet.
	installManagedCheckpoint(t, app, "staged")
	created := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/control/api/v2/api-keys", strings.NewReader(`{"requestId":"`+strings.Repeat("c", 32)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	app.canonicalAPI(created, request)
	if created.Code != http.StatusCreated {
		t.Fatalf("create staged key = %d %s", created.Code, created.Body.String())
	}
	var result freshkeys.Result
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.CreatedSecret == "" {
		t.Fatalf("creation result: %v", err)
	}
	var bindings int
	if err := opened.DB.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_id = ?`, string(result.Key.ID)).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatalf("pre-completion key has %d bindings, want none before the install commits", bindings)
	}

	installManagedCheckpoint(t, app, "committed")
	app.runtime = &installationTestRuntime{status: validInstallationRuntime()}
	sourceCalls, workerStarts := 0, 0
	stub := usageManagementStub(t)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		sourceCalls++
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64)}, nil
	}
	app.startIdentityWorker = func(context.Context) { workerStarts++ }

	if complete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); complete.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
	}
	if sourceCalls != 1 || workerStarts != 1 {
		t.Fatalf("completion started the managed source %d times and the worker %d times, want once each", sourceCalls, workerStarts)
	}
	var firstSeen int64
	if err := opened.DB.QueryRow(`select count(*), coalesce(min(first_seen_at_ms), 0) from gateway_api_key_source_bindings where api_key_id = ?`,
		string(result.Key.ID)).Scan(&bindings, &firstSeen); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 || firstSeen != result.Key.CreatedAtMS {
		t.Fatalf("backfill = %d bindings first seen %d, want one at the key creation %d", bindings, firstSeen, result.Key.CreatedAtMS)
	}
	var startedValue string
	if err := opened.DB.QueryRow(`select value from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedValue); err != nil {
		t.Fatalf("collection start was not recorded: %v", err)
	}
	if startedValue == "" || app.collectionPhase() == usageapp.PhaseStopped {
		t.Fatalf("managed authority did not start collection (phase %s)", app.collectionPhase())
	}

	// The resumed-install path and a replayed completion must not start a second
	// Collector, projection or identity worker.
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app.startUsageCollection(record.Operation)
	if replay := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); replay.Code != http.StatusOK {
		t.Fatalf("completion replay = %d", replay.Code)
	}
	if sourceCalls != 1 || workerStarts != 1 {
		t.Fatalf("repeat startup source %d worker %d, want once each", sourceCalls, workerStarts)
	}
}

// Managed and External installs have no Collector authority of their own. When
// the managed source is unavailable, usage stays stopped instead of falling back
// to the legacy cfg/manager/setup Collector. Only the explicit private profile
// keeps its host-supplied authority.
func TestUsageCollectionNeverFallsBackToLegacyAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	open := func(cfg config.Config) (*fresh.Opened, *freshApplication) {
		t.Helper()
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })
		app := newFreshApplication(opened, cfg, nil)
		app.runCtx = ctx
		return opened, app
	}

	// The legacy cfg authority is present: the removed fallback would have
	// started it.
	opened, app := open(config.Config{CPAUpstreamURL: "http://127.0.0.1:1", ManagementKey: "legacy-management-key"})
	if app.privateCollector != nil {
		t.Fatal("a default install acquired the private Collector authority")
	}
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
	}
	app.startUsageCollection(bootstrap.FreshOperation{ProductComplete: true,
		Strategy: bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, Stage: "committed"}})
	if phase := app.collectionPhase(); phase != usageapp.PhaseStopped {
		t.Fatalf("unavailable managed authority left collection %s, want stopped", phase)
	}
	var started int
	if err := opened.DB.QueryRow(`select count(*) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatal("unavailable managed authority claimed a proven collection start")
	}

	// The explicit private profile still owns its Collector authority.
	t.Setenv("CPAMP_PRIVATE_S1_PERMIT", freshkeyapply.PermitPath)
	_, privateApp := open(config.Config{RuntimeURL: "http://127.0.0.1:9081", CPAUpstreamURL: "http://127.0.0.1:8317"})
	if privateApp.privateCollector == nil {
		t.Fatal("the private profile lost its host-supplied Collector authority")
	}
	if privateApp.managedSource != nil {
		t.Fatal("the private profile acquired the managed install authority")
	}
}

// quietProjection keeps the startup tests free of a background worker on the
// temporary database: they assert the startup chain, not projection itself, and
// a no-repo worker cannot write while the test database is closing.
func quietProjection(app *freshApplication) {
	app.usageProjection = identityprojection.NewWorker(nil)
}

// usageManagementStub is a CPA that answers the one authenticated management
// read a Collector startup requires before it may claim a collection window, and
// an empty usage queue so the real Collector stays live without traffic.
func usageManagementStub(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.13")
			_, _ = w.Write([]byte(`{}`))
		case "/v0/management/usage-queue":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newUsageCompletionApplication builds a routed Fresh app whose usage workers
// run on the caller's host lifetime.
func newUsageCompletionApplication(t *testing.T, ctx context.Context, cfg config.Config) (*fresh.Opened, *freshApplication) {
	t.Helper()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, cfg, nil)
	app.runCtx = ctx
	quietProjection(app)
	if _, err = app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return opened, app
}

func stageClientKey(t *testing.T, app *freshApplication) freshkeys.Result {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/control/api/v2/api-keys", strings.NewReader(`{"requestId":"`+strings.Repeat("f", 32)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create staged key = %d %s", response.Code, response.Body.String())
	}
	var result freshkeys.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.CreatedSecret == "" {
		t.Fatalf("creation result: %v", err)
	}
	return result
}

// The startup runs on the host lifetime, never on the completion request, and a
// failed attempt is retried by a completion replay instead of being consumed.
func TestFreshUsageStartUsesHostLifetimeAndRetriesAfterFailure(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{})

	installManagedCheckpoint(t, app, "staged")
	staged := stageClientKey(t, app)
	installManagedCheckpoint(t, app, "committed")
	app.runtime = &installationTestRuntime{status: validInstallationRuntime()}

	sourceCalls, workerStarts := 0, 0
	var sourceCtx context.Context
	stub := usageManagementStub(t)
	app.managedSource = func(ctx context.Context) (freshkeyapply.ManagedInstall, error) {
		sourceCalls++
		sourceCtx = ctx
		if sourceCalls == 1 {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64)}, nil
	}
	app.startIdentityWorker = func(context.Context) { workerStarts++ }

	if complete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); complete.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
	}
	if sourceCtx != hostCtx {
		t.Fatal("usage startup ran on the completion request instead of the host lifetime")
	}
	if sourceCalls != 1 || workerStarts != 1 {
		t.Fatalf("failed attempt source=%d worker=%d, want 1/1", sourceCalls, workerStarts)
	}
	var bindings int
	var firstSeen int64
	if err := opened.DB.QueryRow(`select count(*), coalesce(min(first_seen_at_ms), 0) from gateway_api_key_source_bindings where api_key_id = ?`,
		string(staged.Key.ID)).Scan(&bindings, &firstSeen); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 || firstSeen != staged.Key.CreatedAtMS {
		t.Fatalf("host-lifetime backfill = %d bindings first seen %d, want one at %d", bindings, firstSeen, staged.Key.CreatedAtMS)
	}

	// A completion replay retries the failed attempt on the same host lifetime.
	if replay := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); replay.Code != http.StatusOK {
		t.Fatalf("replay = %d", replay.Code)
	}
	if sourceCalls != 2 || workerStarts != 1 {
		t.Fatalf("replay source=%d worker=%d, want 2/1 (retried authority, worker not restarted)", sourceCalls, workerStarts)
	}
	var started string
	if err := opened.DB.QueryRow(`select value from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&started); err != nil || started == "" {
		t.Fatalf("retried startup did not record collection: %q err = %v", started, err)
	}
}

// Concurrent triggers serialize: one authority call, one worker start, and no
// overlapping attempt.
func TestFreshUsageStartIsSerializedAcrossConcurrentTriggers(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{})
	installManagedCheckpoint(t, app, "committed")
	app.runtime = &installationTestRuntime{status: validInstallationRuntime()}
	record, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var sourceCalls, workerStarts int32
	var inFlight, maxInFlight int32
	stub := usageManagementStub(t)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		if n := atomic.AddInt32(&inFlight, 1); n > atomic.LoadInt32(&maxInFlight) {
			atomic.StoreInt32(&maxInFlight, n)
		}
		defer atomic.AddInt32(&inFlight, -1)
		atomic.AddInt32(&sourceCalls, 1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64)}, nil
	}
	app.startIdentityWorker = func(context.Context) { atomic.AddInt32(&workerStarts, 1) }

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.startUsageCollection(record.Operation)
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("usage startup never reached the managed authority")
	}
	close(release)
	wg.Wait()

	if calls := atomic.LoadInt32(&sourceCalls); calls != 1 {
		t.Fatalf("managed source started %d times, want once", calls)
	}
	if max := atomic.LoadInt32(&maxInFlight); max != 1 {
		t.Fatalf("management authority attempts overlapped: max in flight %d", max)
	}
	if starts := atomic.LoadInt32(&workerStarts); starts != 1 {
		t.Fatalf("identity worker started %d times, want once", starts)
	}
	var started string
	if err := opened.DB.QueryRow(`select value from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&started); err != nil || started == "" {
		t.Fatalf("startup did not record collection: %q err = %v", started, err)
	}
}

// A durable marker failure is retried without touching the running Collector:
// restarting it would cancel the process and drop whatever arrived meanwhile.
func TestFreshUsageStartRetriesOnlyTheMarkerAfterAMarkerFailure(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{})
	installManagedCheckpoint(t, app, "committed")
	record, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	sourceCalls := 0
	stub := usageManagementStub(t)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		sourceCalls++
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64)}, nil
	}
	collectionStarted := func() int {
		t.Helper()
		var count int
		if err := opened.DB.QueryRow(`select count(*) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// Fail only the durable marker, after the Collector is already running.
	if _, err := opened.DB.Exec(`create trigger fail_collection_start before insert on settings
		when new.key = 'fresh_usage_collection_started_at_ms'
		begin select raise(abort, 'forced marker failure'); end`); err != nil {
		t.Fatal(err)
	}
	app.startUsageCollection(record.Operation)
	if sourceCalls != 1 {
		t.Fatalf("managed source started %d times, want once", sourceCalls)
	}
	if phase := app.collectionPhase(); phase == usageapp.PhaseStopped {
		t.Fatalf("collector phase = %s, want it running before the marker retry", phase)
	}
	if count := collectionStarted(); count != 0 {
		t.Fatalf("marker rows = %d, want none after the forced failure", count)
	}

	// The retry writes the marker only; the Collector must not be restarted.
	if _, err := opened.DB.Exec(`drop trigger fail_collection_start`); err != nil {
		t.Fatal(err)
	}
	app.startUsageCollection(record.Operation)
	if sourceCalls != 1 {
		t.Fatalf("retry restarted the collector: managed source started %d times", sourceCalls)
	}
	if count := collectionStarted(); count != 1 {
		t.Fatalf("marker rows = %d, want exactly one", count)
	}

	// Once everything succeeded the startup is done for good.
	app.startUsageCollection(record.Operation)
	if sourceCalls != 1 {
		t.Fatalf("completed startup started the source again: %d", sourceCalls)
	}
}
