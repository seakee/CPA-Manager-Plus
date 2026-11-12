package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
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

// harnessKeyCreatedAtMS is the creation instant of every Key this harness mints.
// It is a whole hour, and the recorded calls are seeded one second apart from
// it, so the six events can never straddle two hourly buckets: the series
// assertion is about the numbers, not about where in the hour the test ran.
var harnessKeyCreatedAtMS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

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
	// Replace the wall-clock key service with one stamped at a fixed whole hour.
	// Every handler resolves a.keys per request, so the swap covers create,
	// detail and usage reads; nothing in this test mints keys through keyApply.
	harness.app.keys = freshkeys.New(app.keyRepo, opened.Protector,
		freshkeys.WithSourceRuntime(app.installedRuntimeIdentity),
		freshkeys.WithClock(func() int64 { return harnessKeyCreatedAtMS }))
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

	// The service was built with a fixed clock, so creation has to be stamped at
	// that instant rather than at the wall clock. This is what keeps the seeded
	// events inside one hourly bucket no matter when the test runs.
	if harness.key.CreatedAtMS != harnessKeyCreatedAtMS {
		t.Fatalf("key creation instant = %d, want the fixed clock %d", harness.key.CreatedAtMS, harnessKeyCreatedAtMS)
	}

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
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64), Strategy: string(bootstrap.NewManaged)}, nil
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
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64), Strategy: string(bootstrap.NewManaged)}, nil
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
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64), Strategy: string(bootstrap.NewManaged)}, nil
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
		return freshkeyapply.ManagedInstall{BaseURL: stub.URL, ManagementKey: strings.Repeat("a", 64), Strategy: string(bootstrap.NewManaged)}, nil
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

// TestStartupCollectionHonoursStoredMonitoringSwitch is the restart contract:
// the switch the administrator stored governs whether this process starts a
// Collector, so an installation switched off does not come back collecting, an
// enabled one does, and a document that never carried the switch keeps the
// enabled default a completion writes. WAL maintenance is not collection and
// must run either way.
func TestStartupCollectionHonoursStoredMonitoringSwitch(t *testing.T) {
	disabled, enabled := false, true
	for _, scenario := range []struct {
		name    string
		stored  *bool
		collect bool
	}{
		{name: "absent keeps the enabled default", stored: nil, collect: true},
		{name: "enabled", stored: &enabled, collect: true},
		{name: "disabled", stored: &disabled, collect: false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			hostCtx, stopHost := context.WithCancel(t.Context())
			defer stopHost()
			opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{CollectorMode: "http"})
			markFreshProductComplete(t, app)
			app.cfg.DBPath = opened.Record.Operation.Identity.DatabasePath
			stub := usageManagementStub(t)
			app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
				return freshkeyapply.ManagedInstall{
					BaseURL:       stub.URL,
					ManagementKey: strings.Repeat("a", 64),
					Strategy:      string(bootstrap.NewManaged),
				}, nil
			}
			if scenario.stored != nil {
				if err := app.runtimeSettings.SaveManagerConfig(hostCtx, store.ManagerConfig{
					Collector: store.ManagerCollectorConfig{Enabled: scenario.stored},
				}); err != nil {
					t.Fatal(err)
				}
			}
			op, err := app.current(hostCtx)
			if err != nil {
				t.Fatal(err)
			}
			if !op.ProductComplete {
				t.Fatal("the harness did not complete the installation")
			}

			before := app.legacyCollector.StartCount()
			app.startUsageCollection(op)

			if started := app.legacyCollector.StartCount() > before; started != scenario.collect {
				t.Fatalf("collector started = %v, want %v", started, scenario.collect)
			}
			if claimed := app.usageCollectionStarted(); claimed != scenario.collect {
				t.Fatalf("collection claimed = %v, want %v", claimed, scenario.collect)
			}
			var windows int
			if err := opened.DB.QueryRow(`select count(*) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&windows); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario.collect {
				want = 1
			}
			if windows != want {
				t.Fatalf("collection windows = %d, want %d", windows, want)
			}
			if maintenance := freshDatabaseMaintenanceFor(app.cfg.DBPath); maintenance == nil {
				t.Fatal("database maintenance did not start independently of the switch")
			}
			if !scenario.collect {
				reason, _ := app.usageStartReason.Load().(string)
				if reason != usageStartReasonMonitoringDisabled {
					t.Fatalf("blocked reason = %q, want %q", reason, usageStartReasonMonitoringDisabled)
				}
			}
		})
	}
}

// TestUsageMonitoringSwitchWriteStopsAndRestartsCollection is the same contract
// on the running process: the Console writes the switch, and the Collector
// follows it now instead of at the next restart.
func TestUsageMonitoringSwitchWriteStopsAndRestartsCollection(t *testing.T) {
	t.Run("new_managed HTTP PATCH reconciles on host runCtx without double-start or On->On restart", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()
		_, app := newUsageCompletionApplication(t, hostCtx, config.Config{CollectorMode: "http"})
		markFreshProductComplete(t, app)
		stub := usageManagementStub(t)
		app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
			return freshkeyapply.ManagedInstall{
				BaseURL:       stub.URL,
				ManagementKey: strings.Repeat("a", 64),
				Strategy:      string(bootstrap.NewManaged),
			}, nil
		}
		on := true
		if err := app.runtimeSettings.SaveManagerConfig(hostCtx, store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: &on},
		}); err != nil {
			t.Fatal(err)
		}
		op, err := app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		app.startUsageCollection(op)
		if !app.usageCollectionStarted() {
			t.Fatal("collection did not start on an enabled installation")
		}
		initialStarts := app.legacyCollector.StartCount()

		// On -> On idempotent PATCH must not restart the running Collector or
		// detach it onto context.Background().
		same := gatewayRequest(t, app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json")
		if same.Code != http.StatusOK {
			t.Fatalf("PATCH On->On = %d %s", same.Code, same.Body.String())
		}
		if got := app.legacyCollector.StartCount() - initialStarts; got != 0 {
			t.Fatalf("On->On StartCount delta = %d, want 0", got)
		}

		// On -> Off stops immediately.
		off := gatewayRequest(t, app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json")
		if off.Code != http.StatusOK {
			t.Fatalf("PATCH On->Off = %d %s", off.Code, off.Body.String())
		}
		if app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("collector phase = %s after the switch went off", app.collectionPhase())
		}
		if app.usageCollectionStarted() {
			t.Fatal("collection is still claimed after the switch went off")
		}
		if app.usageStartBlock() != usageStartReasonMonitoringDisabled {
			t.Fatalf("blocked reason = %q, want %q", app.usageStartBlock(), usageStartReasonMonitoringDisabled)
		}

		// Off -> On starts exactly once on hostCtx.
		beforeOn := app.legacyCollector.StartCount()
		backOn := gatewayRequest(t, app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json")
		if backOn.Code != http.StatusOK {
			t.Fatalf("PATCH Off->On = %d %s", backOn.Code, backOn.Body.String())
		}
		if got := app.legacyCollector.StartCount() - beforeOn; got != 1 {
			t.Fatalf("Off->On StartCount delta = %d, want 1", got)
		}
		if app.collectionPhase() == usageapp.PhaseStopped || !app.usageCollectionStarted() {
			t.Fatalf("collector did not resume on Off->On: phase=%s claimed=%v", app.collectionPhase(), app.usageCollectionStarted())
		}
	})

	t.Run("connect_external HTTP PATCH toggles local collector without bypassing confirmation or mutating external CPA", func(t *testing.T) {
		h := newExternalUsageHarness(t)

		// 1. External CPA has statistics disabled: PATCH {"enabled": false} -> {"enabled": true}
		// must never write usage-statistics-enabled on the user's CPA.
		off := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`)
		if off.Code != http.StatusOK {
			t.Fatalf("External PATCH false = %d %s", off.Code, off.Body.String())
		}
		if h.app.usageStartBlock() != usageStartReasonMonitoringDisabled {
			t.Fatalf("External blocked reason = %q, want %q", h.app.usageStartBlock(), usageStartReasonMonitoringDisabled)
		}
		// Confirming External usage while Manager monitoring is Off must neither
		// write to CPA nor leave a lingering approval for a later PATCH {"enabled":true}.
		confirmWhileOff := h.call(t, http.MethodPost, "external/usage", `{"requestId":"confirm-while-off","confirmEnableStatistics":true}`)
		if confirmWhileOff.Code != http.StatusOK {
			t.Fatalf("confirm while off = %d %s", confirmWhileOff.Code, confirmWhileOff.Body.String())
		}
		if h.cpa.statWrites.Load() != 0 {
			t.Fatalf("confirm while monitoring off wrote CPA statistics %d times", h.cpa.statWrites.Load())
		}

		onUnconfirmed := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`)
		if onUnconfirmed.Code != http.StatusOK {
			t.Fatalf("External PATCH true (unconfirmed) = %d %s", onUnconfirmed.Code, onUnconfirmed.Body.String())
		}
		if h.cpa.statWrites.Load() != 0 {
			t.Fatalf("External PATCH true mutated CPA usage-statistics-enabled (%d writes)", h.cpa.statWrites.Load())
		}
		if h.app.usageCollectionStarted() {
			t.Fatal("External PATCH true started collection while CPA statistics were disabled")
		}
		if h.app.usageStartBlock() != usageStartReasonStatisticsDisabled {
			t.Fatalf("External blocked reason = %q, want %q", h.app.usageStartBlock(), usageStartReasonStatisticsDisabled)
		}

		// 2. Once statistics are enabled via explicit POST external/usage, PATCH Off -> On
		// stops and resumes collection cleanly with zero additional CPA stat writes.
		confirmed := h.call(t, http.MethodPost, "external/usage", `{"requestId":"confirm-1","confirmEnableStatistics":true}`)
		if confirmed.Code != http.StatusOK || !h.app.usageCollectionStarted() {
			t.Fatalf("explicit confirm = %d %s, started=%v", confirmed.Code, confirmed.Body.String(), h.app.usageCollectionStarted())
		}
		writesAfterConfirm := h.cpa.statWrites.Load()
		startsAfterConfirm := h.app.legacyCollector.StartCount()

		if same := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`); same.Code != http.StatusOK {
			t.Fatalf("External On->On PATCH = %d %s", same.Code, same.Body.String())
		}
		if got := h.app.legacyCollector.StartCount() - startsAfterConfirm; got != 0 {
			t.Fatalf("External On->On StartCount delta = %d, want 0", got)
		}

		if off2 := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`); off2.Code != http.StatusOK {
			t.Fatalf("External On->Off PATCH = %d %s", off2.Code, off2.Body.String())
		}
		if h.app.collectionPhase() != usageapp.PhaseStopped || h.app.usageCollectionStarted() {
			t.Fatalf("External collector still active after Off: phase=%s started=%v", h.app.collectionPhase(), h.app.usageCollectionStarted())
		}

		beforeResume := h.app.legacyCollector.StartCount()
		if resume := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`); resume.Code != http.StatusOK {
			t.Fatalf("External Off->On PATCH = %d %s", resume.Code, resume.Body.String())
		}
		if got := h.app.legacyCollector.StartCount() - beforeResume; got != 1 {
			t.Fatalf("External Off->On StartCount delta = %d, want 1", got)
		}
		if !h.app.usageCollectionStarted() || h.app.collectionPhase() == usageapp.PhaseStopped {
			t.Fatalf("External collector did not resume: phase=%s started=%v", h.app.collectionPhase(), h.app.usageCollectionStarted())
		}
		if h.cpa.statWrites.Load() != writesAfterConfirm {
			t.Fatalf("External Off->On wrote CPA statistics (%d -> %d)", writesAfterConfirm, h.cpa.statWrites.Load())
		}
	})
}

func TestUsageManagerConfigPutReloadsRunningCollectorWithoutRestartingUnchanged(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	initialKey := strings.Repeat("a", 64)
	rotatedKey := strings.Repeat("b", 64)
	var activeKey atomic.Value
	activeKey.Store(initialKey)

	rotatedPollObserved := make(chan string, 1)
	var rotatedPollOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer " + activeKey.Load().(string)
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet:
			w.Header().Set("X-CPA-Version", "v8.0.13")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		case r.URL.Path == "/v0/management/usage-statistics-enabled" && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v0/management/usage-queue" && r.Method == http.MethodGet:
			if r.Header.Get("Authorization") == "Bearer "+rotatedKey {
				count := r.URL.Query().Get("count")
				rotatedPollOnce.Do(func() { rotatedPollObserved <- count })
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	normalizedURL, err := cpa.NormalizeV8BaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{
		CPAUpstreamURL: normalizedURL,
		CollectorMode:  "http",
	})
	protectedInitial, err := opened.Protector.ProtectString(initialKey)
	if err != nil {
		t.Fatal(err)
	}
	current, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeObs := validInstallationRuntime()
	updated, err := app.store.Update(hostCtx, current.Operation.Identity, current.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                string(runtimeObs.Identity),
			TargetGeneration:              uint64(runtimeObs.Generation),
			TargetArtifactID:              string(runtimeObs.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(normalizedURL),
		}
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    normalizedURL,
			ManagementKey: protectedInitial,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := app.store.Update(hostCtx, updated.Operation.Identity, updated.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	app.managedSource = managedInstallSource(app.store, opened.Protector, normalizedURL)

	app.startUsageCollection(completed.Operation)
	if !app.usageCollectionStarted() {
		t.Fatalf("initial collection did not start (reason=%q)", app.usageStartBlock())
	}
	initialStarts := app.legacyCollector.StartCount()

	// 1. Whole-document PUT that leaves effective Collector RuntimeConfig and
	// credential unchanged must not restart the running Collector.
	unchangedBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"enabled":true,"collectorMode":"http","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":500,"queryLimit":25000,"tlsSkipVerify":false}}}`, normalizedURL)
	unchangedResp := gatewayRequest(t, app, http.MethodPut, "cpamp/usage-service/config", unchangedBody, "application/json")
	if unchangedResp.Code != http.StatusOK {
		t.Fatalf("unchanged PUT = %d %s", unchangedResp.Code, unchangedResp.Body.String())
	}
	if got := app.legacyCollector.StartCount() - initialStarts; got != 0 {
		t.Fatalf("unchanged PUT restarted Collector: StartCount delta = %d, want 0", got)
	}

	// 2. Whole-document PUT (via cpamp/config alias) updating queue, pollInterval,
	// batchSize, popSide, tlsSkipVerify and rotating managementKey reloads the
	// running Collector on hostCtx so the next poll uses the new parameters/key.
	activeKey.Store(rotatedKey)
	changedBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q,"managementKey":%q},"collector":{"enabled":true,"collectorMode":"http","queue":"usage-priority","popSide":"left","batchSize":17,"pollIntervalMs":25,"queryLimit":25000,"tlsSkipVerify":true}}}`, normalizedURL, rotatedKey)
	changedResp := gatewayRequest(t, app, http.MethodPut, "cpamp/config", changedBody, "application/json")
	if changedResp.Code != http.StatusOK {
		t.Fatalf("changed PUT = %d %s", changedResp.Code, changedResp.Body.String())
	}
	if got := app.legacyCollector.StartCount() - initialStarts; got != 1 {
		t.Fatalf("changed PUT StartCount delta = %d, want 1", got)
	}
	activeCfg, active := app.legacyCollector.ActiveRuntimeConfig()
	if !active {
		t.Fatal("Collector is not active after changed PUT")
	}
	if activeCfg.ManagementKey != rotatedKey ||
		activeCfg.Queue != "usage-priority" ||
		activeCfg.PopSide != "left" ||
		activeCfg.BatchSize != 17 ||
		activeCfg.PollInterval != 25*time.Millisecond ||
		!activeCfg.TLSSkipVerify {
		t.Fatalf("active runtime config did not pick up PUT update: %+v", activeCfg)
	}
	select {
	case count := <-rotatedPollObserved:
		if count != "17" {
			t.Fatalf("reloaded Collector polled count=%q, want 17", count)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reloaded Collector never polled upstream with rotated management key and updated batch size")
	}

	// 3. Whole-document PUT storing a non-managed-shape credential that fails
	// managedSource stops the running Collector instead of keeping the old run.
	unmanagedKey := strings.Repeat("c", 32)
	activeKey.Store(unmanagedKey)
	unmanagedBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q,"managementKey":%q},"collector":{"enabled":true,"collectorMode":"http","queue":"usage-priority","popSide":"left","batchSize":17,"pollIntervalMs":25,"queryLimit":25000,"tlsSkipVerify":true}}}`, normalizedURL, unmanagedKey)
	unmanagedResp := gatewayRequest(t, app, http.MethodPut, "cpamp/usage-service/config", unmanagedBody, "application/json")
	if unmanagedResp.Code != http.StatusOK {
		t.Fatalf("unmanaged-key PUT = %d %s", unmanagedResp.Code, unmanagedResp.Body.String())
	}
	if app.collectionPhase() != usageapp.PhaseStopped || app.usageCollectionStarted() {
		t.Fatalf("Collector stayed active after authority became unavailable: phase=%s claimed=%v", app.collectionPhase(), app.usageCollectionStarted())
	}
	if app.usageStartBlock() != usageStartReasonNoAuthority {
		t.Fatalf("blocked reason after authority loss = %q, want %q", app.usageStartBlock(), usageStartReasonNoAuthority)
	}

	// 4. Restoring a valid 64-char credential with collector.enabled=false keeps
	// the Collector stopped under usage_monitoring_disabled.
	activeKey.Store(rotatedKey)
	disableBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q,"managementKey":%q},"collector":{"enabled":false,"collectorMode":"http","queue":"usage-priority","popSide":"left","batchSize":17,"pollIntervalMs":25,"queryLimit":25000,"tlsSkipVerify":true}}}`, normalizedURL, rotatedKey)
	disableResp := gatewayRequest(t, app, http.MethodPut, "cpamp/usage-service/config", disableBody, "application/json")
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable PUT = %d %s", disableResp.Code, disableResp.Body.String())
	}
	if app.collectionPhase() != usageapp.PhaseStopped || app.usageCollectionStarted() {
		t.Fatalf("disable PUT left Collector active: phase=%s claimed=%v", app.collectionPhase(), app.usageCollectionStarted())
	}
	if app.usageStartBlock() != usageStartReasonMonitoringDisabled {
		t.Fatalf("blocked reason after disable PUT = %q, want %q", app.usageStartBlock(), usageStartReasonMonitoringDisabled)
	}
}

func TestUsageMonitoringSwitchConcurrentStartAndDisableStopsCollector(t *testing.T) {
	t.Run("disable commits during startup sample and waits on usageStartMu", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var inSample sync.Once
		sampleEntered := make(chan struct{})
		releaseSample := make(chan struct{})
		var sampleCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v0/management/config":
				sampleCalls.Add(1)
				inSample.Do(func() { close(sampleEntered) })
				<-releaseSample
				w.Header().Set("X-CPA-Version", "v8.0.13")
				_, _ = w.Write([]byte(`{}`))
			case "/v0/management/usage-queue":
				_, _ = w.Write([]byte("[]"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		_, app := newUsageCompletionApplication(t, hostCtx, config.Config{CollectorMode: "http"})
		markFreshProductComplete(t, app)
		app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
			return freshkeyapply.ManagedInstall{
				BaseURL:       server.URL,
				ManagementKey: strings.Repeat("a", 64),
				Strategy:      string(bootstrap.NewManaged),
			}, nil
		}
		on := true
		if err := app.runtimeSettings.SaveManagerConfig(hostCtx, store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: &on},
		}); err != nil {
			t.Fatal(err)
		}
		op, err := app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}

		var reconcileHookOnce sync.Once
		patchReadyForLock := make(chan struct{})
		app.beforeUsageReconcileLock = func() {
			reconcileHookOnce.Do(func() { close(patchReadyForLock) })
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			app.startUsageCollection(op)
		}()
		go func() {
			defer wg.Done()
			<-sampleEntered
			resp := gatewayRequest(t, app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json")
			if resp.Code != http.StatusOK {
				t.Errorf("concurrent PATCH false = %d %s", resp.Code, resp.Body.String())
			}
		}()

		select {
		case <-patchReadyForLock:
		case <-time.After(5 * time.Second):
			t.Fatal("PATCH disable did not commit and reach usageStartMu while startup sample was held")
		}
		storedCfg, found, err := app.runtimeSettings.LoadManagerConfig(hostCtx)
		if err != nil || !found || storedCfg.Collector.Enabled == nil || *storedCfg.Collector.Enabled {
			t.Fatalf("expected enabled=false persisted before releasing startup sample, got found=%v cfg=%+v err=%v", found, storedCfg.Collector, err)
		}
		close(releaseSample)
		wg.Wait()

		if got := app.legacyCollector.StartCount(); got != 1 {
			t.Fatalf("StartCount = %d, want 1", got)
		}
		if got := sampleCalls.Load(); got != 1 {
			t.Fatalf("sample calls = %d, want 1", got)
		}
		if app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("concurrent disable left collector phase = %s, want stopped", app.collectionPhase())
		}
		if app.usageCollectionStarted() {
			t.Fatal("concurrent disable left collection claimed")
		}
		if app.usageStartBlock() != usageStartReasonMonitoringDisabled {
			t.Fatalf("blocked reason = %q, want %q", app.usageStartBlock(), usageStartReasonMonitoringDisabled)
		}
	})

	t.Run("disable reconciles before startup acquires usageStartMu", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var remoteCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			remoteCalls.Add(1)
			w.Header().Set("X-CPA-Version", "v8.0.13")
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		_, app := newUsageCompletionApplication(t, hostCtx, config.Config{CollectorMode: "http"})
		markFreshProductComplete(t, app)
		app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
			return freshkeyapply.ManagedInstall{
				BaseURL:       server.URL,
				ManagementKey: strings.Repeat("a", 64),
				Strategy:      string(bootstrap.NewManaged),
			}, nil
		}
		on := true
		if err := app.runtimeSettings.SaveManagerConfig(hostCtx, store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: &on},
		}); err != nil {
			t.Fatal(err)
		}
		op, err := app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}

		reconcileHoldingReady := make(chan struct{})
		releaseReconcile := make(chan struct{})
		var hookOnce sync.Once
		app.beforeUsageReconcileLock = func() {
			hookOnce.Do(func() {
				close(reconcileHoldingReady)
				<-releaseReconcile
			})
		}

		patchDone := make(chan struct{})
		go func() {
			defer close(patchDone)
			resp := gatewayRequest(t, app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json")
			if resp.Code != http.StatusOK {
				t.Errorf("PATCH false = %d %s", resp.Code, resp.Body.String())
			}
		}()

		select {
		case <-reconcileHoldingReady:
		case <-time.After(5 * time.Second):
			t.Fatal("PATCH disable did not reach usageStartMu barrier")
		}
		close(releaseReconcile)
		<-patchDone

		app.startUsageCollection(op)
		if got := app.legacyCollector.StartCount(); got != 0 {
			t.Fatalf("StartCount = %d, want 0 when disable committed first", got)
		}
		if got := remoteCalls.Load(); got != 0 {
			t.Fatalf("remote CPA calls = %d, want 0 when disable committed first", got)
		}
		if app.collectionPhase() != usageapp.PhaseStopped || app.usageCollectionStarted() {
			t.Fatalf("collector started despite disabled switch: phase=%s claimed=%v", app.collectionPhase(), app.usageCollectionStarted())
		}
		if app.usageStartBlock() != usageStartReasonMonitoringDisabled {
			t.Fatalf("blocked reason = %q, want %q", app.usageStartBlock(), usageStartReasonMonitoringDisabled)
		}
	})
}

type bufferedTLSConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedTLSConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

type channelListener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *channelListener) Accept() (net.Conn, error) {
	select {
	case conn, ok := <-l.conns:
		if !ok {
			return nil, net.ErrClosed
		}
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *channelListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *channelListener) Addr() net.Addr {
	return l.addr
}

type selfSignedTLSCPAServer struct {
	url                string
	roots              *cpa.TrustedCARoots
	respAuthed         chan struct{}
	handshakeRejected  chan string
	respAuthCount      atomic.Int32
	handshakeRejectCnt atomic.Int32
	httpConnCnt        atomic.Int32
}

// startSelfSignedTLSCPAServer starts a single-port self-signed TLS endpoint
// multiplexing CPA management HTTP and RESP queue polling (matching CPA's real
// listener). HTTP management clients trust the cert only via context-scoped
// cpa.WithTrustedCARoots (never mutating http.DefaultTransport), whereas
// resp.Dial constructs its own tls.Config with RootCAs: nil and therefore
// succeeds iff RuntimeConfig.TLSSkipVerify is true.
func startSelfSignedTLSCPAServer(t *testing.T, expectedKey string) *selfSignedTLSCPAServer {
	t.Helper()

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.13")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":60}`))
		case "/v0/management/usage-statistics-enabled":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true,"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	})

	certHelper := httptest.NewTLSServer(httpHandler)
	tlsCerts := certHelper.TLS.Certificates
	leafCert := certHelper.Certificate()
	certHelper.Close()

	pool := x509.NewCertPool()
	pool.AddCert(leafCert)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: tlsCerts})
	if err != nil {
		t.Fatal(err)
	}

	roots := cpa.NewTrustedCARoots(pool)
	t.Cleanup(roots.CloseIdleConnections)
	s := &selfSignedTLSCPAServer{
		url:               "https://" + ln.Addr().String(),
		roots:             roots,
		respAuthed:        make(chan struct{}, 16),
		handshakeRejected: make(chan string, 16),
	}
	httpLn := &channelListener{
		addr:  ln.Addr(),
		conns: make(chan net.Conn, 32),
		done:  make(chan struct{}),
	}
	httpSrv := &http.Server{Handler: httpHandler}
	go func() { _ = httpSrv.Serve(httpLn) }()

	t.Cleanup(func() {
		_ = ln.Close()
		_ = httpLn.Close()
		_ = httpSrv.Close()
	})

	go func() {
		for {
			rawConn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				tlsConn := conn.(*tls.Conn)
				_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
				if hsErr := tlsConn.Handshake(); hsErr != nil {
					s.handshakeRejectCnt.Add(1)
					select {
					case s.handshakeRejected <- hsErr.Error():
					default:
					}
					_ = tlsConn.Close()
					return
				}
				_ = tlsConn.SetDeadline(time.Time{})
				br := bufio.NewReader(tlsConn)
				first, peekErr := br.Peek(1)
				if peekErr != nil {
					_ = tlsConn.Close()
					return
				}
				wrapped := &bufferedTLSConn{Conn: tlsConn, r: br}
				if first[0] == '*' {
					serveTestTLSResp(wrapped, br, expectedKey, s)
					return
				}
				s.httpConnCnt.Add(1)
				select {
				case httpLn.conns <- wrapped:
				case <-httpLn.done:
					_ = wrapped.Close()
				}
			}(rawConn)
		}
	}()
	return s
}

func serveTestTLSResp(conn net.Conn, r *bufio.Reader, expectedKey string, s *selfSignedTLSCPAServer) {
	defer conn.Close()
	for {
		args, err := readTestRespCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		switch strings.ToUpper(args[0]) {
		case "AUTH":
			keyArg := args[len(args)-1]
			if keyArg != expectedKey {
				_, _ = io.WriteString(conn, "-WRONGPASS invalid management key\r\n")
				return
			}
			s.respAuthCount.Add(1)
			select {
			case s.respAuthed <- struct{}{}:
			default:
			}
			_, _ = io.WriteString(conn, "+OK\r\n")
		case "RPOP", "LPOP":
			_, _ = io.WriteString(conn, "*-1\r\n")
		default:
			_, _ = io.WriteString(conn, "-ERR unsupported command\r\n")
		}
	}
}

func readTestRespCommand(r *bufio.Reader) ([]string, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	header = strings.TrimSuffix(strings.TrimSuffix(header, "\n"), "\r")
	if !strings.HasPrefix(header, "*") {
		return nil, fmt.Errorf("unexpected RESP header %q", header)
	}
	n, err := strconv.Atoi(header[1:])
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for range n {
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeLine = strings.TrimSuffix(strings.TrimSuffix(sizeLine, "\n"), "\r")
		size, err := strconv.Atoi(strings.TrimPrefix(sizeLine, "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:size]))
	}
	return out, nil
}

func drainChan[T any](ch chan T) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// TestUsageRuntimeConfigPreservesProcessTLSSkipVerifyUntilWholeConfigPut proves
// at the real TLS RESP transport boundary (not just config struct mirrors) that
// USAGE_RESP_TLS_SKIP_VERIFY=true (cfg.TLSSkipVerify=true) allows the Collector
// to complete TLS handshakes and authenticate against a self-signed TLS CPA on
// Fresh-seeded skeleton manager_config_v1 rows (across startup, narrow PATCH
// toggles, and ConnectExternal), whereas an explicit whole-document PUT setting
// tlsSkipVerify=false reloads the Collector and causes TLS certificate
// verification to reject the self-signed endpoint.
func TestUsageRuntimeConfigPreservesProcessTLSSkipVerifyUntilWholeConfigPut(t *testing.T) {
	key := strings.Repeat("d", 64)
	tlsCPA := startSelfSignedTLSCPAServer(t, key)

	// Prove http.DefaultTransport remains untouched: an unscoped default HTTP
	// client must reject the self-signed certificate.
	unscopedClient := &http.Client{Timeout: 2 * time.Second}
	if unscopedRes, unscopedErr := unscopedClient.Get(tlsCPA.url + "/v0/management/config"); unscopedErr == nil {
		_ = unscopedRes.Body.Close()
		t.Fatal("default HTTP transport trusted self-signed certificate without scoped roots")
	}

	hostCtx, stopHost := context.WithCancel(cpa.WithTrustedCARoots(t.Context(), tlsCPA.roots))
	defer stopHost()

	gatewayRequestWithRoots := func(targetApp *freshApplication, method, resource, body, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == "" {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader([]byte(body))
		}
		req := httptest.NewRequest(method, "/control/api/v2/"+resource, reader)
		req = req.WithContext(cpa.WithTrustedCARoots(req.Context(), tlsCPA.roots))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		targetApp.canonicalAPI(rec, req)
		return rec
	}
	normalizedURL, err := cpa.NormalizeV8BaseURL(tlsCPA.url)
	if err != nil {
		t.Fatal(err)
	}
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{
		CPAUpstreamURL: normalizedURL,
		CollectorMode:  "resp",
		PollInterval:   40 * time.Millisecond,
		TLSSkipVerify:  true,
	})
	protectedKey, err := opened.Protector.ProtectString(key)
	if err != nil {
		t.Fatal(err)
	}
	current, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	runtimeObs := validInstallationRuntime()
	updated, err := app.store.Update(hostCtx, current.Operation.Identity, current.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                string(runtimeObs.Identity),
			TargetGeneration:              uint64(runtimeObs.Generation),
			TargetArtifactID:              string(runtimeObs.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(normalizedURL),
		}
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    normalizedURL,
			ManagementKey: protectedKey,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := app.store.Update(hostCtx, updated.Operation.Identity, updated.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	app.managedSource = managedInstallSource(app.store, opened.Protector, normalizedURL)

	awaitTLSRespAccepted := func(step string) {
		t.Helper()
		select {
		case <-tlsCPA.respAuthed:
		case hsErr := <-tlsCPA.handshakeRejected:
			t.Fatalf("%s rejected self-signed TLS certificate despite TLSSkipVerify=true: %s", step, hsErr)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not establish authenticated TLS RESP session (status=%+v)", step, app.legacyCollector.Status())
		}
	}

	// 1. Startup on freshstore-seeded skeleton row establishes real TLS RESP
	// session using process TLSSkipVerify=true, and narrow PATCH Off->On keeps it.
	drainChan(tlsCPA.respAuthed)
	drainChan(tlsCPA.handshakeRejected)
	app.startUsageCollection(completed.Operation)
	awaitTLSRespAccepted("startup on skeleton row")

	if resp := gatewayRequestWithRoots(app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PATCH false = %d %s", resp.Code, resp.Body.String())
	}
	drainChan(tlsCPA.respAuthed)
	drainChan(tlsCPA.handshakeRejected)
	if resp := gatewayRequestWithRoots(app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PATCH true = %d %s", resp.Code, resp.Body.String())
	}
	awaitTLSRespAccepted("narrow PATCH re-enable")

	// 2. Whole-document PUT preserving tlsSkipVerify=true keeps the live TLS RESP
	// session without restarting the Collector.
	startsBeforePut := app.legacyCollector.StartCount()
	httpConnsBeforePuts := tlsCPA.httpConnCnt.Load()
	putTrueBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"enabled":true,"collectorMode":"resp","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":40,"queryLimit":25000,"tlsSkipVerify":true}}}`, normalizedURL)
	if resp := gatewayRequestWithRoots(app, http.MethodPut, "cpamp/usage-service/config", putTrueBody, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT tlsSkipVerify=true = %d %s", resp.Code, resp.Body.String())
	}
	if got := app.legacyCollector.StartCount() - startsBeforePut; got != 0 {
		t.Fatalf("PUT keeping tlsSkipVerify=true restarted Collector: StartCount delta = %d, want 0", got)
	}

	// 3. Whole-document PUT setting tlsSkipVerify=false reloads the Collector,
	// which now rejects the self-signed certificate during the real TLS handshake.
	drainChan(tlsCPA.respAuthed)
	drainChan(tlsCPA.handshakeRejected)
	authsBeforePutFalse := tlsCPA.respAuthCount.Load()
	putFalseBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"enabled":true,"collectorMode":"resp","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":40,"queryLimit":25000,"tlsSkipVerify":false}}}`, normalizedURL)
	if resp := gatewayRequestWithRoots(app, http.MethodPut, "cpamp/usage-service/config", putFalseBody, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT tlsSkipVerify=false = %d %s", resp.Code, resp.Body.String())
	}
	if got := app.legacyCollector.StartCount() - startsBeforePut; got != 1 {
		t.Fatalf("PUT setting tlsSkipVerify=false StartCount delta = %d, want 1", got)
	}
	select {
	case <-tlsCPA.handshakeRejected:
	case <-time.After(5 * time.Second):
		t.Fatalf("Collector did not reject self-signed TLS certificate after PUT tlsSkipVerify=false (status=%+v)", app.legacyCollector.Status())
	}
	if got := tlsCPA.respAuthCount.Load() - authsBeforePutFalse; got != 0 {
		t.Fatalf("Collector authenticated %d RESP sessions despite tlsSkipVerify=false, want 0", got)
	}
	if got := tlsCPA.httpConnCnt.Load() - httpConnsBeforePuts; got != 0 {
		t.Fatalf("pooled TrustedCARoots opened %d new TLS HTTP connections across 2 whole-document PUTs (want 0; expected keep-alive reuse)", got)
	}

	// 4. Stop Managed collector and prove ConnectExternal on a freshstore-seeded
	// skeleton row also establishes a real TLS RESP session when cfg.TLSSkipVerify=true.
	_ = gatewayRequestWithRoots(app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json")
	extApp := newExternalApp(t, normalizedURL, key)
	extHostCtx, stopExtHost := context.WithCancel(cpa.WithTrustedCARoots(t.Context(), tlsCPA.roots))
	defer stopExtHost()
	extApp.runCtx = extHostCtx
	extApp.cfg.CollectorMode = "resp"
	extApp.cfg.PollInterval = 40 * time.Millisecond
	extApp.cfg.TLSSkipVerify = true
	drainChan(tlsCPA.respAuthed)
	drainChan(tlsCPA.handshakeRejected)
	extRecord, err := extApp.store.Load(extHostCtx)
	if err != nil {
		t.Fatal(err)
	}
	extApp.startUsageCollection(extRecord.Operation)
	select {
	case <-tlsCPA.respAuthed:
	case hsErr := <-tlsCPA.handshakeRejected:
		t.Fatalf("ConnectExternal rejected self-signed TLS certificate despite TLSSkipVerify=true: %s", hsErr)
	case <-time.After(5 * time.Second):
		t.Fatalf("ConnectExternal did not establish authenticated TLS RESP session (status=%+v)", extApp.legacyCollector.Status())
	}
}

type delayedReadyUsageRuntime struct {
	mu      sync.Mutex
	status  model.RuntimeObservedStatus
	onStart func()
}

func (r *delayedReadyUsageRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}

func (r *delayedReadyUsageRuntime) setState(state model.RuntimeState) {
	r.mu.Lock()
	r.status.State = state
	r.mu.Unlock()
}

func (r *delayedReadyUsageRuntime) Start(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.status.State = model.RuntimeStateStarting
	status := r.status
	hook := r.onStart
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStart,
		RuntimeIdentity:   status.Identity,
		RuntimeGeneration: status.Generation,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *delayedReadyUsageRuntime) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, errors.New("unexpected Stop")
}
func (r *delayedReadyUsageRuntime) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, errors.New("unexpected Restart")
}
func (r *delayedReadyUsageRuntime) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, errors.New("unexpected PrepareUpdate")
}
func (r *delayedReadyUsageRuntime) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, errors.New("unexpected ActivateUpdate")
}
func (r *delayedReadyUsageRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	return model.RuntimeUpdateOperationObservation{}, errors.New("unexpected ObserveUpdateOperation")
}
func (r *delayedReadyUsageRuntime) StageDestination(context.Context, model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, errors.New("unexpected StageDestination")
}

// After a container restart, the Supervisor begins Offline while the embedded
// Reconciler auto-starts CPA (#1182) and Supervisor Start returns before CPA
// answers its readiness probe. Usage collection started at serveExistingFresh
// must arm once the embedded Runtime reaches Ready when monitoring is enabled,
// and must stay stopped when the stored monitoring switch is Off (#1173).
func TestRestartAutoStartArmsManagedUsageCollectionWhenRuntimeBecomesReady(t *testing.T) {
	disabled := false
	for _, tc := range []struct {
		name                 string
		enabled              *bool
		disableWhileStarting bool
		wantStart            bool
	}{
		{name: "monitoring_default_on_starts_once_ready", enabled: nil, wantStart: true},
		{name: "monitoring_stored_off_stays_stopped_after_ready", enabled: &disabled, wantStart: false},
		{name: "monitoring_switched_off_while_starting_cancels_waiter", enabled: nil, disableWhileStarting: true, wantStart: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostCtx, stopHost := context.WithCancel(t.Context())
			defer stopHost()

			var cpaReady atomic.Bool
			cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !cpaReady.Load() {
					hj, ok := w.(http.Hijacker)
					if ok {
						conn, _, err := hj.Hijack()
						if err == nil {
							_ = conn.Close()
							return
						}
					}
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{}}`))
			}))
			t.Cleanup(cpaServer.Close)

			_, app := newUsageCompletionApplication(t, hostCtx, config.Config{CollectorMode: "http"})
			installManagedCheckpoint(t, app, "committed")
			record, err := app.store.Load(hostCtx)
			if err != nil {
				t.Fatal(err)
			}
			encKey, err := app.protector.ProtectString(strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			baseURL, err := cpa.NormalizeV8BaseURL(cpaServer.URL)
			if err != nil {
				t.Fatal(err)
			}
			artifactID := model.RuntimeArtifactID(record.Operation.Strategy.TargetArtifactID)
			record, err = app.store.Update(hostCtx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Strategy.DestinationConnectionIdentity = freshstrategy.ManagedDestinationIdentity(baseURL)
				r.Connection = &store.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: encKey}
				r.Operation.ProductComplete = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.enabled != nil {
				if _, err := app.runtimeSettings.PatchManagerCollectorEnabled(hostCtx, *tc.enabled); err != nil {
					t.Fatal(err)
				}
			}

			baseStatus := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
			baseStatus.Identity = model.RuntimeIdentity(record.Operation.Strategy.TargetIdentity)
			baseStatus.Generation = 99
			baseStatus.State = model.RuntimeStateOffline
			baseStatus.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifactID}
			baseStatus.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive, AttemptsRemaining: 0}
			rt := &delayedReadyUsageRuntime{status: baseStatus}
			app.runtime = rt
			app.managedSource = managedInstallSource(app.store, app.protector, cpaServer.URL)
			beforeStarts := app.legacyCollector.StartCount()

			// Exact serveExistingFresh startup sequence on container restart:
			// prepareCompletedStartup seeds desired=running while Supervisor is Offline,
			// startUsageCollection runs before CPA finishes starting, and Reconciler
			// auto-starts Supervisor -> Starting -> Ready.
			op := app.prepareCompletedStartup(hostCtx, record.Operation)
			app.startUsageCollection(op)
			if app.usageCollectionStarted() {
				t.Fatal("usage collection claimed started while embedded Runtime was still Offline")
			}

			reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
			if err := reconciler.ReconcileOnce(hostCtx); err != nil {
				t.Fatalf("ReconcileOnce auto-start = %v", err)
			}
			if tc.disableWhileStarting {
				if _, err := app.runtimeSettings.PatchManagerCollectorEnabled(hostCtx, false); err != nil {
					t.Fatal(err)
				}
				app.applyUsageMonitoringSwitch(hostCtx, op)
			}
			// Supervisor Start spawns the OS process (State=Starting); readiness arrives shortly after.
			cpaReady.Store(true)
			rt.setState(model.RuntimeStateReady)

			if tc.wantStart {
				deadline := time.Now().Add(3 * time.Second)
				for !app.usageCollectionStarted() && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				starts := app.legacyCollector.StartCount() - beforeStarts
				if !app.usageCollectionStarted() {
					t.Fatalf("usage collection remained stopped after embedded Runtime auto-started to Ready (reason=%q starts=%d)", app.usageStartBlock(), starts)
				}
				if starts != 1 {
					t.Fatalf("Collector StartCount delta = %d, want 1", starts)
				}
			} else {
				time.Sleep(300 * time.Millisecond)
				starts := app.legacyCollector.StartCount() - beforeStarts
				if app.usageCollectionStarted() || starts != 0 {
					t.Fatalf("stored Off monitoring switch started Collector after Ready: started=%v starts=%d", app.usageCollectionStarted(), starts)
				}
				if got := app.usageStartBlock(); got != usageStartReasonMonitoringDisabled {
					t.Fatalf("usageStartBlock = %q, want %q", got, usageStartReasonMonitoringDisabled)
				}
			}
		})
	}
}
