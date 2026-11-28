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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
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
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
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
		request := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		httpapi.InitialFullWebHandler(currentInstallControlPrefix, http.HandlerFunc(app.canonicalAPI)).ServeHTTP(response, request)
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
		harness.app.clientKeys(response, httptest.NewRequest("GET", currentInstallManagementAPIV2Prefix+""+resource, nil), external, resource)
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
	// A native Manager-created key did not exist before its creation instant:
	// when collection was already running at creation, the 24h read opens at
	// the key's own start and its coverage is complete.
	detail = decodeUsage[usageapp.DetailResponse](t, harness.call("GET", detailPath, ""))
	if detail.Coverage.State != "complete" || detail.Coverage.Reason != "observed" {
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
	if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" ||
		summary.Items[0].Calls != 6 || summary.Items[0].Coverage.Reason != "observed" ||
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
	if summary.Items[0].Coverage.State != "complete" || summary.Items[0].Calls != 6 ||
		summary.Coverage.State != "complete" {
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

func TestFreshKeyUsageSummaryExplicitWindowsAndValidation(t *testing.T) {
	harness := newFreshUsageHarness(t)
	defer harness.cleanup()

	const dayMS = int64(24 * time.Hour / time.Millisecond)
	harness.nowMS = harness.key.CreatedAtMS + 31*dayMS
	harness.useCollection(t, usageapp.PhaseRunning)

	// Seed 2 calls 15d ago (inside 30d, outside 7d) and 3 calls 2d ago (inside both 7d and 30d).
	timestamps := []int64{
		harness.nowMS - 15*dayMS,
		harness.nowMS - 15*dayMS + 1000,
		harness.nowMS - 2*dayMS,
		harness.nowMS - 2*dayMS + 1000,
		harness.nowMS - 2*dayMS + 2000,
	}
	for i, ts := range timestamps {
		eventID := int64(i + 1)
		evHash := sha256Hex(fmt.Sprintf("summary-window-%d", eventID))
		if _, err := harness.db.Exec(`insert into usage_events
			(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
			values (?, ?, ?, ?, '2026-01-15T00:00:00Z', 'gpt-4', ?, 100, 20, 120, 0, ?)`,
			eventID, evHash, fmt.Sprintf("sw-%d", eventID), ts, harness.hash, ts); err != nil {
			t.Fatalf("insert usage event %d: %v", eventID, err)
		}
		if _, err := harness.db.Exec(`insert into gateway_usage_identity_projection_v1
			(usage_event_id, event_hash, request_id, evidence_timestamp_ms, api_key_state, api_key_id, api_key_source_hash,
			 credential_state, credential_id, credential_source_auth_id, schema_version, projected_at_ms)
			values (?, ?, ?, ?, 'mapped', ?, ?, 'unknown', null, '', 1, ?)`,
			eventID, evHash, fmt.Sprintf("sw-%d", eventID), ts, string(harness.key.ID), harness.hash, ts); err != nil {
			t.Fatalf("insert projection %d: %v", eventID, err)
		}
	}
	harness.markProjectionReady(t)

	// Valid 7d window.
	from7d := harness.nowMS - 7*dayMS
	res7d := harness.call("GET", fmt.Sprintf("api-keys/usage-summary?from_ms=%d&to_ms=%d", from7d, harness.nowMS), "")
	if res7d.Code != http.StatusOK {
		t.Fatalf("7d usage-summary = %d %s", res7d.Code, res7d.Body.String())
	}
	sum7d := decodeUsage[usageapp.SummaryResponse](t, res7d)
	if sum7d.FromMS != from7d || sum7d.ToMS != harness.nowMS || len(sum7d.Items) != 1 ||
		sum7d.Items[0].Calls != 3 || sum7d.Items[0].TotalTokens != 360 || len(sum7d.Items[0].RecentSlots) != 20 {
		t.Fatalf("7d usage-summary payload = %+v", sum7d)
	}

	// Valid 30d window.
	from30d := harness.nowMS - 30*dayMS
	res30d := harness.call("GET", fmt.Sprintf("api-keys/usage-summary?from_ms=%d&to_ms=%d", from30d, harness.nowMS), "")
	if res30d.Code != http.StatusOK {
		t.Fatalf("30d usage-summary = %d %s", res30d.Code, res30d.Body.String())
	}
	sum30d := decodeUsage[usageapp.SummaryResponse](t, res30d)
	if sum30d.FromMS != from30d || sum30d.ToMS != harness.nowMS || len(sum30d.Items) != 1 ||
		sum30d.Items[0].Calls != 5 || sum30d.Items[0].TotalTokens != 600 || len(sum30d.Items[0].RecentSlots) != 20 {
		t.Fatalf("30d usage-summary payload = %+v", sum30d)
	}

	// Missing bound -> 400, >31d -> 400, unknown query parameter -> 400.
	for _, badQuery := range []string{
		fmt.Sprintf("api-keys/usage-summary?from_ms=%d", from7d),
		fmt.Sprintf("api-keys/usage-summary?to_ms=%d", harness.nowMS),
		fmt.Sprintf("api-keys/usage-summary?from_ms=%d&to_ms=%d", harness.nowMS-31*dayMS-1, harness.nowMS),
		fmt.Sprintf("api-keys/usage-summary?from_ms=%d&to_ms=%d&granularity=day", from7d, harness.nowMS),
		"api-keys/usage-summary?unknown_param=1",
	} {
		if got := harness.call("GET", badQuery, ""); got.Code != http.StatusBadRequest {
			t.Fatalf("GET %s = %d, want 400", badQuery, got.Code)
		}
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
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Stage the key before the installation commits: it has no attribution yet.
	installManagedCheckpoint(t, app, "staged")
	created := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"api-keys", strings.NewReader(`{"requestId":"`+strings.Repeat("c", 32)+`"}`))
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
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return opened, app
}

func stageClientKey(t *testing.T, app *freshApplication) freshkeys.Result {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"api-keys", strings.NewReader(`{"requestId":"`+strings.Repeat("f", 32)+`"}`))
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
		req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, reader)
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

func TestAdoptExistingCompletionAlignsCollectionStartAndReportsCompleteCoverage(t *testing.T) {
	t.Run("completion aligns collection start and settles to complete zero-call coverage", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx
		// Adopt runtime starts Ready on this fixture.
		fixture.runtime.status.State = model.RuntimeStateReady

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}

		keys, err := fixture.app.keys.List(hostCtx)
		if err != nil || len(keys) != 2 {
			t.Fatalf("keys after adopt complete = %#v %v", keys, err)
		}
		var startedMS, admittedMS int64
		if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedMS); err != nil {
			t.Fatalf("read collection start: %v", err)
		}
		if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_gateway_admitted_at_ms'`).Scan(&admittedMS); err != nil {
			t.Fatalf("read gateway admitted at: %v", err)
		}
		if startedMS <= 0 || admittedMS <= 0 || startedMS > admittedMS {
			t.Fatalf("fresh_usage_collection_started_at_ms = %d, fresh_gateway_admitted_at_ms = %d, want 0 < startedMS <= admittedMS", startedMS, admittedMS)
		}

		// Wait for Collector's first tick + projection catch-up (and >=1ms past collectingAt).
		var summary usageapp.SummaryResponse
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			if resp.Code != http.StatusOK {
				t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
				t.Fatalf("decode usage-summary: %v", err)
			}
			if summary.Coverage.State == "complete" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
			t.Fatalf("adopt post-completion summary coverage = %+v, want complete/observed", summary.Coverage)
		}
		if len(summary.Items) != 2 {
			t.Fatalf("summary items = %d, want 2", len(summary.Items))
		}
		for i, item := range summary.Items {
			if item.Coverage.State != "complete" || item.Coverage.Reason != "observed" || item.Calls != 0 {
				t.Fatalf("summary item[%d] = %+v, want complete/observed 0 calls", i, item)
			}
			if item.EstimatedCostUSD != nil && *item.EstimatedCostUSD != 0 {
				t.Fatalf("summary item[%d].EstimatedCostUSD = %v, want nil or 0", i, *item.EstimatedCostUSD)
			}
		}

		detResp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/"+string(keys[0].ID)+"/usage", "", "")
		if detResp.Code != http.StatusOK {
			t.Fatalf("detail usage = %d %s", detResp.Code, detResp.Body.String())
		}
		var detail usageapp.DetailResponse
		if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decode detail usage: %v", err)
		}
		if detail.Coverage.State != "complete" || detail.Coverage.Reason != "observed" {
			t.Fatalf("adopt post-completion detail coverage = %+v, want complete/observed", detail.Coverage)
		}
		expectedFromMS := max(startedMS, keys[0].CreatedAtMS)
		if detail.FromMS != expectedFromMS {
			t.Fatalf("detail.FromMS = %d, want max(startedMS=%d, key CreatedAtMS=%d) = %d", detail.FromMS, startedMS, keys[0].CreatedAtMS, expectedFromMS)
		}
		if detail.Totals == nil || detail.Totals.Calls != 0 || (detail.Totals.EstimatedCostUSD != nil && *detail.Totals.EstimatedCostUSD != 0) {
			t.Fatalf("detail totals = %+v, want Calls=0 Cost=0/nil", detail.Totals)
		}
	})

	t.Run("monitoring disabled at adoption completion stays unproven and later re-enable stays partial", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		// Hold /v0/management/config off during complete so ProductComplete commits
		// before the Collector can prove itself.
		fixture.destination.serveUsageAPI = false
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx
		fixture.runtime.status.State = model.RuntimeStateReady

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		// Now disable monitoring and invoke startUsageCollectionAtCompletion with
		// the usage endpoint ready: !enabled must refuse to start, clear
		// pendingInitialUsageCompletion, and leave no collection start marker.
		if _, err := fixture.app.runtimeSettings.PatchManagerCollectorEnabled(hostCtx, false); err != nil {
			t.Fatal(err)
		}
		fixture.destination.serveUsageAPI = true
		op, err := fixture.app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		fixture.app.startUsageCollection(op)
		var count int
		if err := fixture.opened.DB.QueryRow(`select count(*) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("monitoring-disabled completion recorded %d collection start markers, want 0", count)
		}

		// Re-enable monitoring >=15ms after completion so collectingAt > FirstSeenAtMS.
		time.Sleep(15 * time.Millisecond)
		if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
			t.Fatalf("PATCH enabled=true = %d %s", resp.Code, resp.Body.String())
		}
		var summary usageapp.SummaryResponse
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			if resp.Code != http.StatusOK {
				t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Coverage.State == "partial" || summary.Coverage.State == "complete" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if summary.Coverage.State != "partial" || summary.Coverage.Reason != "window_partial" {
			t.Fatalf("delayed re-enable coverage = %+v, want partial/window_partial", summary.Coverage)
		}
	})

	t.Run("non-adopt strategies never opt into adopted pre-binding proof", func(t *testing.T) {
		ctx := t.Context()

		// ConnectExternal committed harness: predicate MUST be false.
		extHarness := newExternalUsageHarness(t)
		if extHarness.app.isAdoptedPreBindingProven(ctx) {
			t.Fatal("isAdoptedPreBindingProven(ConnectExternal) = true, want false")
		}

		// NewManaged committed harness: predicate MUST be false.
		managedHarness := newFreshUsageHarness(t)
		defer managedHarness.cleanup()
		if managedHarness.app.isAdoptedPreBindingProven(ctx) {
			t.Fatal("isAdoptedPreBindingProven(NewManaged) = true, want false")
		}

		// Staged (uncommitted) AdoptExisting: predicate MUST be false.
		stagedAdopt := newAdoptMigrationFixtureWithClientKeys(t, "source-client-one")
		if stagedAdopt.app.isAdoptedPreBindingProven(ctx) {
			t.Fatal("isAdoptedPreBindingProven(adopt staged) = true, want false")
		}

		// Upgrade (no Fresh operation record): a.current fails closed -> predicate MUST be false.
		if _, err := stagedAdopt.opened.DB.ExecContext(ctx, `delete from settings where key = 'bootstrap_state_v1'`); err != nil {
			t.Fatal(err)
		}
		if stagedAdopt.app.isAdoptedPreBindingProven(ctx) {
			t.Fatal("isAdoptedPreBindingProven(upgrade/non-fresh) = true, want false")
		}
	})
}

func TestAdoptExistingAsyncReadinessPreservesInitialCompletionAlignment(t *testing.T) {
	t.Run("runtime not ready at completion preserves initial alignment once ready", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx

		baseStatus := fixture.runtime.status
		baseStatus.State = model.RuntimeStateStarting
		delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
		fixture.app.runtime = delayedRT

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		if fixture.app.usageCollectionStarted() {
			t.Fatal("usage collection claimed started while runtime was still Starting")
		}

		// While runtime is still Starting and the usage waiter is armed, usage endpoints
		// must surface retryable unknown/collection_starting rather than collection_unproven.
		sumWhileStarting := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
		if sumWhileStarting.Code != http.StatusOK {
			t.Fatalf("usage-summary while Starting = %d %s", sumWhileStarting.Code, sumWhileStarting.Body.String())
		}
		var startingSummary usageapp.SummaryResponse
		if err := json.Unmarshal(sumWhileStarting.Body.Bytes(), &startingSummary); err != nil {
			t.Fatal(err)
		}
		if startingSummary.Coverage.State != "unknown" || startingSummary.Coverage.Reason != "collection_starting" {
			t.Fatalf("usage-summary while Starting = %+v, want unknown/collection_starting", startingSummary.Coverage)
		}
		keysWhileStarting, err := fixture.app.keys.List(hostCtx)
		if err != nil || len(keysWhileStarting) != 2 {
			t.Fatalf("keys while Starting = %#v %v", keysWhileStarting, err)
		}
		detWhileStarting := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/"+string(keysWhileStarting[0].ID)+"/usage", "", "")
		if detWhileStarting.Code != http.StatusOK {
			t.Fatalf("detail usage while Starting = %d %s", detWhileStarting.Code, detWhileStarting.Body.String())
		}
		var startingDetail usageapp.DetailResponse
		if err := json.Unmarshal(detWhileStarting.Body.Bytes(), &startingDetail); err != nil {
			t.Fatal(err)
		}
		if startingDetail.Coverage.State != "unknown" || startingDetail.Coverage.Reason != "collection_starting" {
			t.Fatalf("detail usage while Starting = %+v, want unknown/collection_starting", startingDetail.Coverage)
		}

		// Simulate runtime taking a moment to transition to Ready.
		time.Sleep(50 * time.Millisecond)
		delayedRT.setState(model.RuntimeStateReady)

		deadline := time.Now().Add(3 * time.Second)
		for !fixture.app.usageCollectionStarted() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !fixture.app.usageCollectionStarted() {
			t.Fatalf("usage collection did not start after Ready (reason=%q)", fixture.app.usageStartBlock())
		}

		keys, err := fixture.app.keys.List(hostCtx)
		if err != nil || len(keys) != 2 {
			t.Fatalf("keys = %#v %v", keys, err)
		}
		var startedMS, admittedMS int64
		if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedMS); err != nil {
			t.Fatal(err)
		}
		if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_gateway_admitted_at_ms'`).Scan(&admittedMS); err != nil {
			t.Fatal(err)
		}
		if startedMS != admittedMS {
			t.Fatalf("delayed Ready aligned collection start = %d, want gateway admitted at %d", startedMS, admittedMS)
		}

		var summary usageapp.SummaryResponse
		sumDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(sumDeadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			if resp.Code != http.StatusOK {
				t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Coverage.State == "complete" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
			t.Fatalf("delayed Ready summary coverage = %+v, want complete/observed", summary.Coverage)
		}
	})

	t.Run("disabling monitoring while waiting clears pending initial completion flag", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx

		baseStatus := fixture.runtime.status
		baseStatus.State = model.RuntimeStateStarting
		delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
		fixture.app.runtime = delayedRT

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		var waitSummary usageapp.SummaryResponse
		if resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", ""); resp.Code != http.StatusOK {
			t.Fatalf("usage-summary while waiter armed = %d %s", resp.Code, resp.Body.String())
		} else if err := json.Unmarshal(resp.Body.Bytes(), &waitSummary); err != nil {
			t.Fatal(err)
		} else if waitSummary.Coverage.State != "unknown" || waitSummary.Coverage.Reason != "collection_starting" {
			t.Fatalf("usage-summary while waiter armed = %+v, want unknown/collection_starting", waitSummary.Coverage)
		}
		// Turn monitoring off while runtime is still Starting.
		if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json"); resp.Code != http.StatusOK {
			t.Fatalf("PATCH enabled=false = %d %s", resp.Code, resp.Body.String())
		}
		var offSummary usageapp.SummaryResponse
		if resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", ""); resp.Code != http.StatusOK {
			t.Fatalf("usage-summary after disabling while waiting = %d %s", resp.Code, resp.Body.String())
		} else if err := json.Unmarshal(resp.Body.Bytes(), &offSummary); err != nil {
			t.Fatal(err)
		} else if offSummary.Coverage.State != "unknown" || offSummary.Coverage.Reason != "collection_unproven" {
			t.Fatalf("usage-summary after disabling while waiting = %+v, want unknown/collection_unproven", offSummary.Coverage)
		}
		delayedRT.setState(model.RuntimeStateReady)
		time.Sleep(30 * time.Millisecond)

		// Re-enabling later must NOT inherit pendingInitialUsageCompletion.
		if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
			t.Fatalf("PATCH enabled=true = %d %s", resp.Code, resp.Body.String())
		}
		var summary usageapp.SummaryResponse
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			if resp.Code != http.StatusOK {
				t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Coverage.State == "partial" || summary.Coverage.State == "complete" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if summary.Coverage.State != "partial" || summary.Coverage.Reason != "window_partial" {
			t.Fatalf("re-enabled after cancel coverage = %+v, want partial/window_partial", summary.Coverage)
		}
	})
}

func TestAdoptExistingQueuedUsageEventDuringStartup(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx
	fixture.runtime.status.State = model.RuntimeStateReady

	poll1Started := make(chan struct{})
	allowPoll1 := make(chan struct{})
	var poll1Once sync.Once
	fixture.destination.onUsageQueuePoll = func(poll int) {
		if poll == 1 {
			poll1Once.Do(func() {
				close(poll1Started)
				<-allowPoll1
			})
		}
	}
	fixture.app.beforeCompletionUsageStart = func() {
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		event := fmt.Sprintf(`{"timestamp":%q,"model":"gpt-test","endpoint":"POST /v1/chat/completions","api_key":"source-client-one","input_tokens":120,"output_tokens":30,"total_tokens":150}`, ts)
		fixture.destination.usageQueueMu.Lock()
		fixture.destination.usageQueue = append(fixture.destination.usageQueue, event)
		fixture.destination.usageQueueMu.Unlock()
	}

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	select {
	case <-poll1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("collector never attempted initial usage-queue poll")
	}

	// While the very first poll is in-flight, usage-summary must surface unknown/collection_starting
	// instead of falsely reporting complete with 0 calls.
	var inflightSummary usageapp.SummaryResponse
	if resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", ""); resp.Code != http.StatusOK {
		t.Fatalf("in-flight usage-summary = %d %s", resp.Code, resp.Body.String())
	} else if err := json.Unmarshal(resp.Body.Bytes(), &inflightSummary); err != nil {
		t.Fatal(err)
	}
	if inflightSummary.Coverage.State != "unknown" || inflightSummary.Coverage.Reason != "collection_starting" {
		t.Fatalf("in-flight usage-summary coverage = %+v, want unknown/collection_starting", inflightSummary.Coverage)
	}

	close(allowPoll1)

	var summary usageapp.SummaryResponse
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Coverage.State == "complete" && len(summary.Items) == 1 && summary.Items[0].Calls == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
		t.Fatalf("settled summary coverage = %+v, want complete/observed", summary.Coverage)
	}
	if len(summary.Items) != 1 || summary.Items[0].Calls != 1 {
		t.Fatalf("settled summary items = %+v, want 1 item with calls=1", summary.Items)
	}

	keys, err := fixture.app.keys.List(hostCtx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %#v %v", keys, err)
	}
	var detail usageapp.DetailResponse
	if resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/"+string(keys[0].ID)+"/usage", "", ""); resp.Code != http.StatusOK {
		t.Fatalf("detail usage = %d %s", resp.Code, resp.Body.String())
	} else if err := json.Unmarshal(resp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Coverage.State != "complete" || detail.Coverage.Reason != "observed" || detail.Totals == nil || detail.Totals.Calls != 1 {
		t.Fatalf("settled detail = %+v (totals=%+v), want complete/observed calls=1", detail.Coverage, detail.Totals)
	}
}

func TestAdoptExistingBarrierCompletionDisabledPlusConcurrentEnable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		enableViaPatch bool
	}{
		{name: "concurrent PATCH enabled=true races ahead of usageStartMu", enableViaPatch: true},
		{name: "concurrent config flip enabled=true before usageStartMu still trips seq fence", enableViaPatch: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostCtx, stopHost := context.WithCancel(t.Context())
			defer stopHost()

			fixture := adoptionFixtureWithKeys(t, "source-client-one")
			fixture.destination.serveUsageAPI = true
			fixture.app.cfg.CollectorMode = "http"
			fixture.app.cfg.PollInterval = 20 * time.Millisecond
			fixture.app.runCtx = hostCtx
			fixture.runtime.status.State = model.RuntimeStateReady

			fixture.app.beforeCompletionUsageStart = func() {
				if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json"); resp.Code != http.StatusOK {
					t.Fatalf("disable at completion = %d %s", resp.Code, resp.Body.String())
				}
			}
			var startOnce sync.Once
			fixture.app.beforeUsageStartLock = func() {
				startOnce.Do(func() {
					time.Sleep(15 * time.Millisecond)
					if tc.enableViaPatch {
						if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
							t.Fatalf("concurrent PATCH enabled=true = %d %s", resp.Code, resp.Body.String())
						}
					} else {
						if _, err := fixture.app.runtimeSettings.PatchManagerCollectorEnabled(hostCtx, true); err != nil {
							t.Fatalf("PatchManagerCollectorEnabled(true): %v", err)
						}
					}
				})
			}

			if complete := fixture.complete(t); complete.Code != http.StatusOK {
				t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
			}

			keys, err := fixture.app.keys.List(hostCtx)
			if err != nil || len(keys) != 1 {
				t.Fatalf("keys = %#v %v", keys, err)
			}
			var startedMS int64
			if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedMS); err != nil {
				t.Fatalf("read collection start: %v", err)
			}
			if startedMS <= keys[0].CreatedAtMS {
				t.Fatalf("collection start %d was backdated <= CreatedAtMS %d despite completion disable + concurrent enable", startedMS, keys[0].CreatedAtMS)
			}

			var summary usageapp.SummaryResponse
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
				if resp.Code != http.StatusOK {
					t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
					t.Fatal(err)
				}
				if summary.Coverage.State == "partial" || summary.Coverage.State == "complete" {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if summary.Coverage.State != "partial" || summary.Coverage.Reason != "window_partial" {
				t.Fatalf("barrier(a) coverage = %+v, want partial/window_partial", summary.Coverage)
			}
		})
	}
}

func TestAdoptExistingBarrierWaiterDisabledPairBeforeLock(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx

	baseStatus := fixture.runtime.status
	baseStatus.State = model.RuntimeStateStarting
	delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
	fixture.app.runtime = delayedRT

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	keys, err := fixture.app.keys.List(hostCtx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %#v %v", keys, err)
	}

	offPaused := make(chan struct{})
	releaseOff := make(chan struct{})
	offDone := make(chan struct{})
	var offGate atomic.Bool
	fixture.app.beforeUsageReconcileLock = func() {
		if offGate.CompareAndSwap(false, true) {
			close(offPaused)
			<-releaseOff
		}
	}

	go func() {
		resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json")
		if resp.Code != http.StatusOK {
			t.Errorf("PATCH enabled=false = %d %s", resp.Code, resp.Body.String())
		}
		close(offDone)
	}()
	<-offPaused

	// While OFF is paused right before usageStartMu (with disableSeq + gap already committed),
	// re-enable monitoring and bring runtime to Ready so the armed initial-completion waiter
	// enters startUsageCollectionFromWaiter BEFORE OFF acquires usageStartMu.
	time.Sleep(15 * time.Millisecond)
	if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("concurrent PATCH enabled=true = %d %s", resp.Code, resp.Body.String())
	}
	delayedRT.setState(model.RuntimeStateReady)

	deadline := time.Now().Add(3 * time.Second)
	for !fixture.app.usageCollectionStarted() && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if !fixture.app.usageCollectionStarted() {
		close(releaseOff)
		<-offDone
		t.Fatalf("waiter did not start collector after Ready (reason=%q)", fixture.app.usageStartBlock())
	}
	close(releaseOff)
	<-offDone

	var startedMS int64
	if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedMS); err != nil {
		t.Fatalf("read collection start: %v", err)
	}
	if startedMS <= keys[0].CreatedAtMS {
		t.Fatalf("waiter backdated collection start %d <= CreatedAtMS %d despite OFF->ON disable race", startedMS, keys[0].CreatedAtMS)
	}

	var summary usageapp.SummaryResponse
	sumDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(sumDeadline) {
		resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Coverage.State == "partial" || summary.Coverage.State == "complete" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if summary.Coverage.State != "partial" || summary.Coverage.Reason != "window_partial" {
		t.Fatalf("barrier(b) coverage = %+v, want partial/window_partial", summary.Coverage)
	}
}

func TestAdoptExistingMonitoringGapAdvancesCollectionStartAcrossPatchAndPut(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx
	fixture.runtime.status.State = model.RuntimeStateReady

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitSettled := func(wantState, wantReason string) usageapp.SummaryResponse {
		t.Helper()
		var summary usageapp.SummaryResponse
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			if resp.Code != http.StatusOK {
				t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Coverage.State == wantState && summary.Coverage.Reason == wantReason {
				return summary
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s/%s, last=%+v", wantState, wantReason, summary.Coverage)
		return summary
	}
	readStartedMS := func() int64 {
		t.Helper()
		var startedMS int64
		if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&startedMS); err != nil {
			t.Fatalf("read collection start: %v", err)
		}
		return startedMS
	}

	waitSettled("complete", "observed")
	t0 := readStartedMS()

	// 1. Turn monitoring OFF via PATCH, wait >=15ms, then ON via PATCH -> T0 must advance forward.
	if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":false}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PATCH enabled=false = %d %s", resp.Code, resp.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	if resp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PATCH enabled=true = %d %s", resp.Code, resp.Body.String())
	}
	t1 := readStartedMS()
	if t1 <= t0 {
		t.Fatalf("PATCH gap did not advance collection start: t0=%d t1=%d", t0, t1)
	}
	waitSettled("partial", "window_partial")

	// 2. Turn monitoring OFF via nested PUT config, wait >=15ms, then ON via PUT -> T1 must advance forward to T2.
	time.Sleep(20 * time.Millisecond)
	if resp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", `{"config":{"collector":{"enabled":false}}}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT enabled=false = %d %s", resp.Code, resp.Body.String())
	}
	if fixture.app.usageCollectionStarted() {
		t.Fatal("PUT config.collector.enabled=false left usage collection started")
	}
	time.Sleep(20 * time.Millisecond)
	if resp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", `{"config":{"collector":{"enabled":true}}}`, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT enabled=true = %d %s", resp.Code, resp.Body.String())
	}
	t2 := readStartedMS()
	if t2 <= t1 {
		t.Fatalf("PUT gap did not advance collection start: t1=%d t2=%d", t1, t2)
	}
	waitSettled("partial", "window_partial")
}

func TestAdoptExistingRestartWithMonitoringOnPreservesCollectionStart(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	fixture := adoptionFixtureWithKeys(t, "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx
	fixture.runtime.status.State = model.RuntimeStateReady

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		stopHost()
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	var t0 int64
	if err := fixture.opened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&t0); err != nil {
		stopHost()
		t.Fatalf("read initial collection start: %v", err)
	}

	// Shut down first instance cleanly and wait >20ms so restart wall clock strictly exceeds T0.
	stopHost()
	if err := fixture.opened.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	time.Sleep(25 * time.Millisecond)

	hostCtx2, stopHost2 := context.WithCancel(t.Context())
	defer stopHost2()
	reopened, err := fresh.Open(hostCtx2, fixture.paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	app2 := newFreshApplication(reopened, config.Config{
		CollectorMode: "http",
		PollInterval:  20 * time.Millisecond,
	}, nil)
	app2.runCtx = hostCtx2
	wireAdoptManagedAuthority(app2, fixture.runtime, fixture.destination.URL())
	record2, err := app2.store.Load(hostCtx2)
	if err != nil {
		t.Fatalf("load reopened store: %v", err)
	}
	op2 := app2.prepareCompletedStartup(hostCtx2, record2.Operation)
	app2.startUsageCollection(op2)

	deadline := time.Now().Add(3 * time.Second)
	for !app2.usageCollectionStarted() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !app2.usageCollectionStarted() {
		t.Fatalf("restarted app did not start usage collection (reason=%q)", app2.usageStartBlock())
	}

	var afterRestartMS int64
	if err := reopened.DB.QueryRow(`select cast(value as integer) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&afterRestartMS); err != nil {
		t.Fatalf("read collection start after restart: %v", err)
	}
	if afterRestartMS != t0 {
		t.Fatalf("normal restart altered collection start from %d to %d", t0, afterRestartMS)
	}

	var summary usageapp.SummaryResponse
	sumDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(sumDeadline) {
		resp := gatewayRequest(t, app2, http.MethodGet, "api-keys/usage-summary", "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("restarted usage-summary = %d %s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Coverage.State == "complete" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
		t.Fatalf("normal restart coverage = %+v, want complete/observed", summary.Coverage)
	}
}

// dualProtoCPAServer multiplexes HTTP management/queue traffic and RESP Pub/Sub
// on one TCP address, matching CPA's dual-protocol port in Fresh tests.
type dualProtoCPAServer struct {
	listener       net.Listener
	httpHandler    http.Handler
	httpListener   *chanNetListener
	httpServer     *http.Server
	mu             sync.Mutex
	subConns       []net.Conn
	subChannels    []string
	subCount       atomic.Int32
	onSubscribe    func(connID int32)
	dropOnConnID   int32
	dropCh         chan struct{}
	dropOnce       sync.Once
	subReadyCh     chan struct{}
	subReadyOnce   sync.Once
	secondSubCh    chan struct{}
	rejectSubscribe atomic.Bool
	secondSubOnce  sync.Once
}

type chanNetListener struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *chanNetListener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.conns:
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *chanNetListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *chanNetListener) Addr() net.Addr { return l.addr }

type peekedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func newDualProtoCPAServer(t *testing.T, handler http.Handler) *dualProtoCPAServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpLn := &chanNetListener{
		addr:   ln.Addr(),
		conns:  make(chan net.Conn, 64),
		closed: make(chan struct{}),
	}
	s := &dualProtoCPAServer{
		listener:     ln,
		httpHandler:  handler,
		httpListener: httpLn,
		dropCh:       make(chan struct{}),
		subReadyCh:   make(chan struct{}),
		secondSubCh:  make(chan struct{}),
	}
	s.httpServer = &http.Server{Handler: handler}
	go func() { _ = s.httpServer.Serve(httpLn) }()
	go s.acceptLoop()
	t.Cleanup(func() {
		_ = s.httpServer.Close()
		_ = httpLn.Close()
		_ = ln.Close()
		s.mu.Lock()
		for _, c := range s.subConns {
			_ = c.Close()
		}
		s.mu.Unlock()
	})
	return s
}

func (s *dualProtoCPAServer) URL() string { return "http://" + s.listener.Addr().String() }

func (s *dualProtoCPAServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			br := bufio.NewReader(c)
			first, err := br.Peek(1)
			if err != nil {
				_ = c.Close()
				return
			}
			pc := &peekedConn{Conn: c, reader: br}
			if first[0] != '*' {
				select {
				case s.httpListener.conns <- pc:
				case <-s.httpListener.closed:
					_ = c.Close()
				}
				return
			}
			s.handleRESP(pc, br)
		}(conn)
	}
}

func (s *dualProtoCPAServer) handleRESP(conn net.Conn, reader *bufio.Reader) {
	defer func() { _ = conn.Close() }()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "*") {
			continue
		}
		n := 0
		_, _ = fmt.Sscanf(strings.TrimSpace(line), "*%d", &n)
		parts := make([]string, 0, n)
		for range n {
			_, err = reader.ReadString('\n')
			if err != nil {
				return
			}
			arg, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			parts = append(parts, strings.TrimSpace(arg))
		}
		if len(parts) == 0 {
			continue
		}
		switch strings.ToUpper(parts[0]) {
		case "AUTH":
			_, _ = io.WriteString(conn, "+OK\r\n")
		case "PING":
			_, _ = io.WriteString(conn, "+PONG\r\n")
		case "RPOP", "LPOP":
			_, _ = io.WriteString(conn, "*-1\r\n")
		case "SUBSCRIBE":
			ch := "usage"
			if len(parts) > 1 {
				ch = parts[1]
			}
			if s.rejectSubscribe.Load() {
				_, _ = io.WriteString(conn, "-ERR subscribe temporarily unavailable\r\n")
				_ = conn.Close()
				return
			}
			id := s.subCount.Add(1)
			s.mu.Lock()
			s.subConns = append(s.subConns, conn)
			s.subChannels = append(s.subChannels, ch)
			s.mu.Unlock()
			_, _ = fmt.Fprintf(conn, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(ch), ch)
			s.subReadyOnce.Do(func() { close(s.subReadyCh) })
			if id >= 2 {
				s.secondSubOnce.Do(func() { close(s.secondSubCh) })
			}
			if s.onSubscribe != nil {
				s.onSubscribe(id)
			}
			if s.dropOnConnID == id {
				go func() {
					<-s.dropCh
					_ = conn.Close()
				}()
			}
		default:
			_, _ = io.WriteString(conn, "-ERR unsupported\r\n")
		}
	}
}

func (s *dualProtoCPAServer) Publish(payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.subConns {
		ch := s.subChannels[i]
		_, _ = fmt.Fprintf(c, "*3\r\n$7\r\nmessage\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(ch), ch, len(payload), payload)
	}
}

func (s *dualProtoCPAServer) DropConn(connID int32) {
	s.dropOnce.Do(func() { close(s.dropCh) })
}

func adoptionFixtureWithDestination(t *testing.T, makeEndpoint func(*migrationFakeCPA) string, clientKeys ...string) (*adoptMigrationFixture, string) {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{
		Identity: "runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}}
	fixture := &adoptMigrationFixture{runtime: runtime, paths: paths, opened: opened}

	source := newMigrationFakeCPA(t)
	source.auth = migrationSourceKey
	source.credentials["claude-source.json"] = []byte(`{"type":"claude","access_token":"` + migrationCredentialToken + `"}`)
	source.types["claude-source.json"] = "claude"
	source.credentials["codex-source.json"] = []byte(`{"type":"codex","access_token":"` + migrationCredentialToken + `-codex"}`)
	source.types["codex-source.json"], source.disabled["codex-source.json"] = "codex", true
	source.credentials["plugin-source.json"] = []byte(`{"runtime":true}`)
	source.types["plugin-source.json"], source.runtimeOnly["plugin-source.json"] = "plugin", true
	source.config["routing"] = map[string]any{"strategy": "round-robin", "force-model-prefix": true}
	source.config["multimedia"] = map[string]any{"disable-image-generation": "chat"}
	source.config["api-keys"] = map[string]any{"claude": []any{map[string]any{
		"name": "claude-group", "keys": []any{map[string]any{"api-key": migrationProviderKeyValue}},
	}}}
	source.config["management"] = map[string]any{"secret-key": migrationSourceKey}
	source.config["server"] = map[string]any{"host": "203.0.113.9", "port": json.Number("9999")}
	sourceKeys := make([]any, 0, len(clientKeys))
	for _, key := range clientKeys {
		sourceKeys = append(sourceKeys, key)
	}
	source.config["access"] = map[string]any{"api-keys": sourceKeys}
	source.config["plugins"] = map[string]any{"enabled": true, "dir": "/source/plugins"}
	source.config["oauth"] = map[string]any{"auth-dir": "/source/auth"}
	fixture.source = source

	destination := newMigrationFakeCPA(t)
	destination.serveUsageAPI = true
	destination.refuse["unsupported.json"] = true
	destination.config["server"] = map[string]any{"host": "0.0.0.0", "port": json.Number("8317")}
	destination.config["plugins"] = map[string]any{"enabled": false, "dir": "/runtime/supervisor/plugins"}
	destination.config["oauth"] = map[string]any{"auth-dir": "/runtime/supervisor/destination/auth"}
	destination.config["config-version"] = json.Number("8")
	destination.config["management"] = map[string]any{"allow-remote": true, "secret-key": "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12", "disable-control-panel": true, "disable-auto-update-panel": true}
	destination.config["observability"] = map[string]any{"logs": map[string]any{"logging-to-file": false}, "usage": map[string]any{"usage-statistics-enabled": true}}
	destinationKeys := make([]any, 0, len(clientKeys))
	for _, key := range clientKeys {
		destinationKeys = append(destinationKeys, key)
	}
	destination.config["access"] = map[string]any{"api-keys": destinationKeys}
	destination.authFn = func() string { return runtime.stagedKey }
	fixture.destination = destination
	endpoint := makeEndpoint(destination)

	app := newFreshApplication(opened, config.Config{}, nil)
	fixture.app = app
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: endpoint,
		Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		QualifiedArtifactID: artifact, AdoptionResources: migratingDecisions(),
	})
	fixture.request = freshstrategy.Request{
		Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting,
		BaseURL: source.URL(), ManagementKey: migrationSourceKey,
		ExpectedRuntimeIdentity: "runtime", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact,
	}
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := app.strategy.Execute(ctx, record.Operation, fixture.request)
	if err != nil || staged.Checkpoint.Stage != "staged" {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	fixture.session = installationSession(t, app)
	record, err = app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err = app.store.Update(ctx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = staged.Checkpoint
		r.AdoptionCiphertext = staged.AdoptionCiphertext
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.InferenceTarget = "/cpamp-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.record = record
	wireAdoptManagedAuthority(app, runtime, endpoint)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	if summary := fixture.reload(t).AdoptionSummary; summary == nil || !summary.Finished {
		t.Fatalf("the migration did not record a finished run: %+v", summary)
	}
	return fixture, endpoint
}

func newNewManagedSubscribeFixture(t *testing.T, hostCtx context.Context, handler http.Handler, rtState model.RuntimeState, stagePreKey bool) (*fresh.Opened, *freshApplication, *delayedReadyUsageRuntime, *dualProtoCPAServer, freshkeys.Result) {
	t.Helper()
	dual := newDualProtoCPAServer(t, handler)
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(hostCtx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{
		CollectorMode: "subscribe",
		Queue:         "usage",
		PollInterval:  20 * time.Millisecond,
	}, nil)
	app.runCtx = hostCtx
	if _, err = app.store.Update(hostCtx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	installManagedCheckpoint(t, app, "committed")
	baseStatus := validInstallationRuntime()
	baseStatus.State = rtState
	rt := &delayedReadyUsageRuntime{status: baseStatus}
	app.runtime = rt
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{
			BaseURL:       dual.URL(),
			ManagementKey: strings.Repeat("a", 64),
			Strategy:      string(bootstrap.NewManaged),
		}, nil
	}
	var key freshkeys.Result
	if stagePreKey {
		key = stageClientKey(t, app)
	}
	return opened, app, rt, dual, key
}

func readSettingInt64(t *testing.T, db *sql.DB, key string) (int64, bool) {
	t.Helper()
	var raw string
	err := db.QueryRow(`select value from settings where key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("query setting %s: %v", key, err)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		t.Fatalf("parse setting %s=%q: %v", key, raw, err)
	}
	return parsed, true
}

func readSettingString(t *testing.T, db *sql.DB, key string) (string, bool) {
	t.Helper()
	var raw string
	err := db.QueryRow(`select value from settings where key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("query setting %s: %v", key, err)
	}
	return raw, true
}

func waitUsageSummaryCoverage(t *testing.T, app *freshApplication, wantState, wantReason string) usageapp.SummaryResponse {
	t.Helper()
	var summary usageapp.SummaryResponse
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := gatewayRequest(t, app, http.MethodGet, "api-keys/usage-summary", "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("usage-summary = %d %s", resp.Code, resp.Body.String())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &summary); err != nil {
			t.Fatalf("decode usage-summary: %v", err)
		}
		if summary.Coverage.State == wantState && (wantReason == "" || summary.Coverage.Reason == wantReason) {
			return summary
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("usage-summary coverage = %+v, want %s/%s", summary.Coverage, wantState, wantReason)
	return summary
}

func TestAdoptExistingAndNewManagedSubscribePreAdmissionWarmStart(t *testing.T) {
	t.Run("AdoptExisting subscribe pre-admission warm-start achieves complete coverage and cleans up on rollback", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var dual *dualProtoCPAServer
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			dual = newDualProtoCPAServer(t, http.HandlerFunc(dest.serve))
			return dual.URL()
		}, "source-client-two", "source-client-one")
		fixture.app.cfg.CollectorMode = "subscribe"
		fixture.app.cfg.Queue = "usage"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx

		// Verify UpdateTx failure rolls back the warm-started Collector cleanly with zero SQLite pollution.
		if _, err := fixture.opened.DB.Exec(fmt.Sprintf(`create trigger fail_adopt_complete before update on settings when new.key = '%s' begin select raise(abort, 'forced adopt update failure'); end`, freshstore.OperationKey)); err != nil {
			t.Fatal(err)
		}
		badResp := fixture.complete(t)
		if badResp.Code < 400 {
			t.Fatalf("forced adopt complete failure = %d %s, want >= 400", badResp.Code, badResp.Body.String())
		}
		if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok {
			t.Fatal("failed adopt/complete leaked fresh_usage_collection_started_at_ms")
		}
		if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms"); ok {
			t.Fatal("failed adopt/complete leaked fresh_gateway_admitted_at_ms")
		}
		if fixture.app.usageCollectionStarted() || fixture.app.rawCollectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("failed adopt/complete left Collector running (started=%v phase=%s)", fixture.app.usageCollectionStarted(), fixture.app.rawCollectionPhase())
		}
		if _, err := fixture.opened.DB.Exec(`drop trigger fail_adopt_complete`); err != nil {
			t.Fatal(err)
		}

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}

		startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= 0 {
			t.Fatalf("missing fresh_usage_collection_started_at_ms")
		}
		admittedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")
		if !ok || admittedMS <= 0 {
			t.Fatalf("missing fresh_gateway_admitted_at_ms")
		}
		if startedMS > admittedMS {
			t.Fatalf("subscribe pre-admission startedMS=%d > admittedMS=%d", startedMS, admittedMS)
		}
		var minFirstSeen int64
		if err := fixture.opened.DB.QueryRow(`select min(first_seen_at_ms) from gateway_api_key_source_bindings where retired_at_ms is null`).Scan(&minFirstSeen); err != nil {
			t.Fatal(err)
		}
		if startedMS > minFirstSeen {
			t.Fatalf("subscribe pre-admission startedMS=%d > minFirstSeen=%d", startedMS, minFirstSeen)
		}

		summary := waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
		if len(summary.Items) != 2 {
			t.Fatalf("summary items = %d, want 2", len(summary.Items))
		}
		ts := time.Now().UTC().Add(10 * time.Millisecond).Format(time.RFC3339Nano)
		dual.Publish(fmt.Sprintf(`{"timestamp":%q,"model":"gpt-test","endpoint":"POST /v1/chat/completions","api_key":"source-client-one","input_tokens":50,"output_tokens":10,"total_tokens":60}`, ts))

		deadline := time.Now().Add(3 * time.Second)
		var totalCalls int64
		for time.Now().Before(deadline) {
			resp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
			_ = json.Unmarshal(resp.Body.Bytes(), &summary)
			totalCalls = 0
			for _, it := range summary.Items {
				totalCalls += it.Calls
			}
			if totalCalls == 1 && summary.Coverage.State == "complete" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if totalCalls != 1 || summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
			t.Fatalf("subscribe post-completion summary calls=%d coverage=%+v, want 1 complete/observed", totalCalls, summary.Coverage)
		}
	})

	t.Run("NewManaged subscribe pre-admission warm-start achieves complete coverage and cleans up on rollback", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		stub := usageManagementStub(t)
		opened, app, _, dual, _ := newNewManagedSubscribeFixture(t, hostCtx, stub.Config.Handler, model.RuntimeStateReady, false)

		// Simulate UpdateTx failure after warm-start via SQLite trigger on fresh_operation_v1.
		if _, err := opened.DB.Exec(fmt.Sprintf(`create trigger fail_managed_complete before update on settings when new.key = '%s' begin select raise(abort, 'forced managed update failure'); end`, freshstore.OperationKey)); err != nil {
			t.Fatal(err)
		}
		badResp := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
		if badResp.Code != http.StatusConflict {
			t.Fatalf("bad complete = %d %s, want 409", badResp.Code, badResp.Body.String())
		}
		if _, err := opened.DB.Exec(`drop trigger fail_managed_complete`); err != nil {
			t.Fatal(err)
		}
		if _, ok := readSettingInt64(t, opened.DB, "fresh_usage_collection_started_at_ms"); ok {
			t.Fatal("failed NewManaged complete leaked fresh_usage_collection_started_at_ms")
		}
		if app.usageCollectionStarted() || app.rawCollectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("failed NewManaged complete left Collector running")
		}

		if complete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); complete.Code != http.StatusOK {
			t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
		}
		startedMS, ok := readSettingInt64(t, opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= 0 {
			t.Fatal("missing fresh_usage_collection_started_at_ms")
		}
		admittedMS, ok := readSettingInt64(t, opened.DB, "fresh_gateway_admitted_at_ms")
		if !ok || admittedMS <= 0 {
			t.Fatal("missing fresh_gateway_admitted_at_ms")
		}
		if startedMS > admittedMS {
			t.Fatalf("NewManaged subscribe pre-admission startedMS=%d > admittedMS=%d", startedMS, admittedMS)
		}
		postKey := stageClientKey(t, app)
		_ = dual
		waitUsageSummaryCoverage(t, app, "complete", "observed")
		detResp := gatewayRequest(t, app, http.MethodGet, "api-keys/"+string(postKey.Key.ID)+"/usage", "", "")
		var detail usageapp.DetailResponse
		if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Coverage.State != "complete" || detail.Coverage.Reason != "observed" {
			t.Fatalf("NewManaged subscribe post-completion key coverage = %+v, want complete/observed", detail.Coverage)
		}
	})

	t.Run("NewManaged subscribe does not backdate over a key staged before subscribe observed", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		stub := usageManagementStub(t)
		_, app, _, _, preKey := newNewManagedSubscribeFixture(t, hostCtx, stub.Config.Handler, model.RuntimeStateReady, true)
		time.Sleep(15 * time.Millisecond)
		if complete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); complete.Code != http.StatusOK {
			t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
		}
		waitUsageSummaryCoverage(t, app, "partial", "window_partial")
		detResp := gatewayRequest(t, app, http.MethodGet, "api-keys/"+string(preKey.Key.ID)+"/usage", "", "")
		var detail usageapp.DetailResponse
		if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Coverage.State != "partial" || detail.Coverage.Reason != "window_partial" {
			t.Fatalf("pre-warmstart staged key under subscribe = %+v, want partial/window_partial", detail.Coverage)
		}
	})
}

func TestAdoptExistingAndNewManagedSubscribePostAdmissionReportsPartial(t *testing.T) {
	t.Run("AdoptExisting subscribe post-admission startup is never backdated and stays partial/window_partial", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var dual *dualProtoCPAServer
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			dual = newDualProtoCPAServer(t, http.HandlerFunc(dest.serve))
			return dual.URL()
		}, "source-client-two", "source-client-one")
		fixture.app.cfg.CollectorMode = "subscribe"
		fixture.app.cfg.Queue = "usage"
		fixture.app.cfg.PollInterval = 20 * time.Millisecond
		fixture.app.runCtx = hostCtx
		baseStatus := fixture.runtime.status
		baseStatus.State = model.RuntimeStateStarting
		delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
		fixture.app.runtime = delayedRT
		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		admittedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")
		if !ok || admittedMS <= 0 {
			t.Fatal("missing fresh_gateway_admitted_at_ms")
		}
		time.Sleep(25 * time.Millisecond)
		delayedRT.setState(model.RuntimeStateReady)

		summary := waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
		startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= admittedMS {
			t.Fatalf("post-admission subscribe startedMS=%d <= admittedMS=%d (must not backdate)", startedMS, admittedMS)
		}
		keys, _ := fixture.app.keys.List(hostCtx)
		detResp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/"+string(keys[0].ID)+"/usage", "", "")
		var detail usageapp.DetailResponse
		if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Coverage.State != "partial" || detail.Coverage.Reason != "window_partial" {
			t.Fatalf("AdoptExisting post-admission subscribe detail coverage = %+v, want partial/window_partial (summary=%+v)", detail.Coverage, summary.Coverage)
		}
	})

	t.Run("NewManaged subscribe post-admission startup is never backdated and stays partial/window_partial", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		stub := usageManagementStub(t)
		opened, app, _, dual, keys := newNewManagedSubscribeFixture(t, hostCtx, stub.Config.Handler, model.RuntimeStateReady, true)
		dual.rejectSubscribe.Store(true)

		if complete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin); complete.Code != http.StatusOK {
			t.Fatalf("complete = %d %s", complete.Code, complete.Body.String())
		}
		admittedMS, ok := readSettingInt64(t, opened.DB, "fresh_gateway_admitted_at_ms")
		if !ok || admittedMS <= 0 {
			t.Fatal("missing fresh_gateway_admitted_at_ms")
		}
		time.Sleep(25 * time.Millisecond)
		dual.rejectSubscribe.Store(false)
		waitUsageSummaryCoverage(t, app, "partial", "window_partial")
		startedMS, ok := readSettingInt64(t, opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= admittedMS {
			t.Fatalf("NewManaged post-admission subscribe startedMS=%d <= admittedMS=%d", startedMS, admittedMS)
		}
		detResp := gatewayRequest(t, app, http.MethodGet, "api-keys/"+string(keys.Key.ID)+"/usage", "", "")
		var detail usageapp.DetailResponse
		if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Coverage.State != "partial" || detail.Coverage.Reason != "window_partial" {
			t.Fatalf("NewManaged post-admission subscribe detail coverage = %+v, want partial/window_partial", detail.Coverage)
		}
	})
}

func TestSubscribeDisconnectAdvancesCollectionStartOnReconnect(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	var dual *dualProtoCPAServer
	fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
		dual = newDualProtoCPAServer(t, http.HandlerFunc(dest.serve))
		dual.dropOnConnID = 1
		return dual.URL()
	}, "source-client-two", "source-client-one")
	fixture.app.cfg.CollectorMode = "subscribe"
	fixture.app.cfg.Queue = "usage"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx
	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok {
		t.Fatal("missing initial fresh_usage_collection_started_at_ms")
	}
	admittedMS, _ := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")

	time.Sleep(25 * time.Millisecond)
	dual.DropConn(1)

	select {
	case <-dual.secondSubCh:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not reconnect after drop")
	}
	summary := waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
	t1, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok || t1 <= t0 || t1 <= admittedMS {
		t.Fatalf("reconnect collection start = %d, want > t0(%d) and > admittedMS(%d)", t1, t0, admittedMS)
	}
	if gap, hasGap := readSettingString(t, fixture.opened.DB, "fresh_usage_unreplayed_gap"); hasGap && gap == "1" {
		t.Fatal("fresh_usage_unreplayed_gap remained set after reconnect observed")
	}
	_ = summary
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestHTTPRetainedQueueOutageThreshold(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	var failNextDial atomic.Bool
	var failNext500 atomic.Bool
	errServed := make(chan struct{}, 4)
	fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
		muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/usage-queue" && failNext500.CompareAndSwap(true, false) {
				http.Error(w, "post-pop server error", http.StatusInternalServerError)
				select {
				case errServed <- struct{}{}:
				default:
				}
				return
			}
			dest.serve(w, r)
		}))
		t.Cleanup(muxSrv.Close)
		return muxSrv.URL
	}, "source-client-two", "source-client-one")
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(baseTransport.CloseIdleConnections)
	fixture.app.legacyCollector.SetHTTPClient(&http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/usage-queue") && failNextDial.CompareAndSwap(true, false) {
				select {
				case errServed <- struct{}{}:
				default:
				}
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
			}
			return baseTransport.RoundTrip(req)
		}),
	})
	var clockMS atomic.Int64
	clockMS.Store(time.Now().UnixMilli())
	fixture.app.legacyCollector.SetNowFunc(clockMS.Load)
	fixture.app.usage.SetClock(func() int64 { return clockMS.Load() + 1000 })

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok {
		t.Fatal("missing initial collection start")
	}

	// Outage <= 45,000ms (pre-pop dial error) preserves retained queue continuity.
	clockMS.Add(30_000)
	failNextDial.Store(true)
	<-errServed
	deadline := time.Now().Add(3 * time.Second)
	for fixture.app.rawCollectionPhase() != usageapp.PhaseError && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for fixture.app.rawCollectionPhase() != usageapp.PhaseRunning && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	t1, _ := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if t1 != t0 {
		t.Fatalf("<=45s HTTP outage altered startedMS from %d to %d", t0, t1)
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")

	// Outage > 45,000ms breaches retained queue window -> advances marker & degrades to partial/window_partial.
	clockMS.Add(50_000)
	failNextDial.Store(true)
	<-errServed
	var t2 int64
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && cur > t0 {
			t2 = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if t2 <= t0 {
		t.Fatalf(">45s HTTP outage startedMS=%d <= initial=%d", t2, t0)
	}
	waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")

	// Uncertain HTTP 500 error advances marker even within 1ms.
	clockMS.Add(1)
	failNext500.Store(true)
	<-errServed
	var t3 int64
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && cur > t2 {
			t3 = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if t3 <= t2 {
		t.Fatalf("uncertain HTTP 500 did not advance startedMS (%d <= %d)", t3, t2)
	}
}

func TestAdoptExistingDBCommitBeforeHTTPReturnGapStillComplete(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx
	baseStatus := fixture.runtime.status
	baseStatus.State = model.RuntimeStateStarting
	delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
	wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
	fixture.app.runtime = delayedRT

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	admittedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")
	if !ok || admittedMS <= 0 {
		t.Fatal("missing fresh_gateway_admitted_at_ms")
	}
	// Simulate runtime becoming Ready shortly after UpdateTx commit (well within 45s retained queue).
	time.Sleep(25 * time.Millisecond)
	delayedRT.setState(model.RuntimeStateReady)

	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok || startedMS != admittedMS {
		t.Fatalf("HTTP retained queue alignment = %d, want gateway admitted %d", startedMS, admittedMS)
	}
}

func TestPutUsageConfigOmittedOrNullCollectorEnabledPreservesMonitoring(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 20 * time.Millisecond
	fixture.app.runCtx = hostCtx
	fixture.runtime.status.State = model.RuntimeStateReady
	wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok {
		t.Fatal("missing initial fresh_usage_collection_started_at_ms")
	}

	// 1. PUT with collector omitted must preserve monitoring=true and zero disabled gap.
	putOmitted := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q}}}`, fixture.destination.URL())
	if resp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", putOmitted, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT omitted collector = %d %s", resp.Code, resp.Body.String())
	}
	if _, hasGap := readSettingString(t, fixture.opened.DB, "fresh_usage_monitoring_disabled_gap"); hasGap {
		t.Fatal("PUT omitted collector created fresh_usage_monitoring_disabled_gap")
	}
	if seq, hasSeq := readSettingInt64(t, fixture.opened.DB, "fresh_usage_monitoring_disable_seq"); hasSeq && seq != 0 {
		t.Fatalf("PUT omitted collector incremented disable seq to %d", seq)
	}
	if t1, _ := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); t1 != t0 {
		t.Fatalf("PUT omitted collector changed startedMS from %d to %d", t0, t1)
	}

	// 2. PUT with collector.enabled: null must preserve monitoring=true and zero disabled gap.
	putNull := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"enabled":null,"queue":"usage"}}}`, fixture.destination.URL())
	if resp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", putNull, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT null collector.enabled = %d %s", resp.Code, resp.Body.String())
	}
	if _, hasGap := readSettingString(t, fixture.opened.DB, "fresh_usage_monitoring_disabled_gap"); hasGap {
		t.Fatal("PUT null collector.enabled created fresh_usage_monitoring_disabled_gap")
	}
	if t2, _ := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); t2 != t0 {
		t.Fatalf("PUT null collector.enabled changed startedMS from %d to %d", t0, t2)
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")

	// 3. Failing PUT (invalid json / bad payload) with disabled token must NOT write gap or stop Collector.
	badPut := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", `{"config":{"collector":{"enabled":false`, "application/json")
	if badPut.Code < 400 {
		t.Fatalf("bad PUT = %d, want 4xx", badPut.Code)
	}
	if _, hasGap := readSettingString(t, fixture.opened.DB, "fresh_usage_monitoring_disabled_gap"); hasGap {
		t.Fatal("failed PUT created fresh_usage_monitoring_disabled_gap")
	}
	if !fixture.app.usageCollectionStarted() || fixture.app.rawCollectionPhase() == usageapp.PhaseStopped {
		t.Fatal("failed PUT stopped Collector")
	}
}

func TestWaiterToCollectorHandoffRemainsCollectionStartingUntilMarkerCommits(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx
	baseStatus := fixture.runtime.status
	baseStatus.State = model.RuntimeStateStarting
	delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
	wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
	fixture.app.runtime = delayedRT

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	if !fixture.app.hasArmedManagedUsageRuntimeWaiter() {
		t.Fatal("expected waiter armed before Ready")
	}
	waitUsageSummaryCoverage(t, fixture.app, "unknown", "collection_starting")

	markerEntered := make(chan struct{})
	releaseMarker := make(chan struct{})
	var enterOnce sync.Once
	fixture.app.beforeUsageMarkerCommit = func() {
		enterOnce.Do(func() {
			close(markerEntered)
			<-releaseMarker
		})
	}

	delayedRT.setState(model.RuntimeStateReady)
	select {
	case <-markerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Collector observation marker commit hook")
	}

	if _, hasMarker := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); hasMarker {
		t.Fatal("collection start marker committed before hook released")
	}
	// Even after waiter cleared usageAwaitArmed and Collector started running,
	// Summary and Detail MUST report collection_starting until marker commits.
	var summary usageapp.SummaryResponse
	sumResp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/usage-summary", "", "")
	if sumResp.Code != http.StatusOK {
		t.Fatalf("handoff usage-summary = %d %s", sumResp.Code, sumResp.Body.String())
	}
	if err := json.Unmarshal(sumResp.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Coverage.State != "unknown" || summary.Coverage.Reason != "collection_starting" {
		t.Fatalf("handoff summary coverage = %+v, want unknown/collection_starting (never collection_unproven)", summary.Coverage)
	}

	keys, err := fixture.app.keys.List(hostCtx)
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys = %#v %v", keys, err)
	}
	detResp := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys/"+string(keys[0].ID)+"/usage", "", "")
	if detResp.Code != http.StatusOK {
		t.Fatalf("handoff detail = %d %s", detResp.Code, detResp.Body.String())
	}
	var detail usageapp.DetailResponse
	if err := json.Unmarshal(detResp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Coverage.State != "unknown" || detail.Coverage.Reason != "collection_starting" {
		t.Fatalf("handoff detail coverage = %+v, want unknown/collection_starting", detail.Coverage)
	}

	close(releaseMarker)
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
}

func TestSampleUsageAuthorityFailureReleasesUsageStartMuDuringAuthRejection(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	var sampleCalls atomic.Int32
	authRejectingCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/usage-queue" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sampleCalls.Add(1)
		http.Error(w, "temporary error", http.StatusBadGateway)
	}))
	defer authRejectingCPA.Close()

	fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 5 * time.Millisecond
	fixture.app.runCtx = hostCtx
	fixture.runtime.status.State = model.RuntimeStateReady
	fixture.app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{
			BaseURL:       authRejectingCPA.URL,
			ManagementKey: strings.Repeat("a", 64),
			Strategy:      string(bootstrap.AdoptExisting),
		}, nil
	}

	done := make(chan warmStartUsageState, 1)
	op, _ := fixture.app.current(hostCtx)
	go func() {
		done <- fixture.app.warmStartUsageCollectorBeforeCompletion(hostCtx, op)
	}()

	select {
	case warm := <-done:
		if warm.started || !warm.sampleErr {
			t.Fatalf("warmStart state = %+v, want started=false sampleErr=true", warm)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("warmStartUsageCollectorBeforeCompletion deadlocked while stopping Collector that invokes reportCollectorAuthRejection")
	}
	if fixture.app.usageStartupPending.Load() {
		t.Fatal("usageStartupPending remained true after sampleUsageAuthority failure")
	}
	if sampleCalls.Load() == 0 {
		t.Fatal("expected sampleUsageAuthority call")
	}

	// Also verify attemptUsageStartWithWarmState when called directly (post-admission path).
	doneStart := make(chan bool, 1)
	go func() {
		fixture.app.usageStartMu.Lock()
		defer fixture.app.usageStartMu.Unlock()
		doneStart <- fixture.app.attemptUsageStartWithWarmState(hostCtx, op, false, warmStartUsageState{})
	}()
	select {
	case ok := <-doneStart:
		if ok {
			t.Fatal("attemptUsageStartWithWarmState = true, want false on sample error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attemptUsageStartWithWarmState deadlocked while stopping Collector that invokes reportCollectorAuthRejection")
	}
	if fixture.app.usageStartupPending.Load() {
		t.Fatal("usageStartupPending remained true after attemptUsageStartWithWarmState sample failure")
	}
}

func TestStaleObservationFenceRejectsDisableSeqRaceWithoutClearingDisabledGap(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
	fixture.destination.serveUsageAPI = true
	fixture.app.cfg.CollectorMode = "http"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx
	baseStatus := fixture.runtime.status
	baseStatus.State = model.RuntimeStateStarting
	delayedRT := &delayedReadyUsageRuntime{status: baseStatus}
	wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
	fixture.app.runtime = delayedRT

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}

	obsEntered := make(chan struct{})
	releaseObs := make(chan struct{})
	var once sync.Once
	fixture.app.beforeUsageMarkerCommit = func() {
		once.Do(func() {
			close(obsEntered)
			<-releaseObs
		})
	}

	delayedRT.setState(model.RuntimeStateReady)
	select {
	case <-obsEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial observation commit hook")
	}

	// While Run 1's observation (snapshotted at disable_seq=0) is paused before SQLite commit,
	// toggle monitoring OFF (bumps disable_seq to 1, sets disabled_gap=1) and back ON without
	// cancelling Run 1 yet — SQLite must fence Run 1's observation via ObservedDisableSeq.
	if _, err := fixture.app.runtimeSettings.PatchManagerCollectorEnabled(hostCtx, false); err != nil {
		t.Fatal(err)
	}
	fixture.app.beforeUsageMarkerCommit = nil
	close(releaseObs)

	if gap, ok := readSettingString(t, fixture.opened.DB, "fresh_usage_monitoring_disabled_gap"); !ok || gap != "1" {
		t.Fatalf("stale seq=0 observation cleared fresh_usage_monitoring_disabled_gap (gap=%q ok=%v)", gap, ok)
	}
	if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok {
		t.Fatal("stale seq=0 observation wrote fresh_usage_collection_started_at_ms while monitoring disabled")
	}

	// Now re-enable monitoring; the fresh run (seq=1) commits cleanly from the new instant.
	time.Sleep(25 * time.Millisecond)
	patchResp := gatewayRequest(t, fixture.app, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`, "application/json")
	if patchResp.Code != http.StatusOK {
		t.Fatalf("PATCH config enabled=true = %d %s", patchResp.Code, patchResp.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
}

func TestSubscribeToHTTPConfigReloadAdvancesCollectionStart(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	var dual *dualProtoCPAServer
	fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
		dual = newDualProtoCPAServer(t, http.HandlerFunc(dest.serve))
		return dual.URL()
	}, "source-client-two", "source-client-one")
	fixture.app.cfg.CollectorMode = "subscribe"
	fixture.app.cfg.Queue = "usage"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok {
		t.Fatal("missing initial subscribe collection start")
	}
	transport0, _ := readSettingString(t, fixture.opened.DB, "fresh_usage_last_transport")
	if transport0 != "subscribe" {
		t.Fatalf("last_transport = %q, want subscribe", transport0)
	}

	time.Sleep(25 * time.Millisecond)
	// Reload collector mode from subscribe -> http via manager config PUT.
	fixture.app.cfg.CollectorMode = "http"
	putBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"collectorMode":"http","pollIntervalMs":15}}}`, dual.URL())
	putResp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", putBody, "application/json")
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT mode=http = %d %s", putResp.Code, putResp.Body.String())
	}
	var t1 int64
	var transport1 string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		curT, curOk := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		curTr, _ := readSettingString(t, fixture.opened.DB, "fresh_usage_last_transport")
		if curOk && curT > t0 && curTr == "http" {
			t1 = curT
			transport1 = curTr
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if t1 <= t0 || transport1 != "http" {
		t.Fatalf("subscribe -> http reload = (t1=%d <= t0=%d, transport=%q), want t1 > t0 and http", t1, t0, transport1)
	}
	waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
}

func TestUsageConfigRetentionBudgetAndUnverifiedFailClosed(t *testing.T) {
	t.Run("honors 10s CPA retention (7500ms budget) across short and long pre-pop outages", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var failNextDial atomic.Bool
		errServed := make(chan struct{}, 4)
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-CPA-Version", "v8.0.13")
					_, _ = io.WriteString(w, `{"redis-usage-queue-retention-seconds":10}`)
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(muxSrv.Close)
			return muxSrv.URL
		}, "source-client-two", "source-client-one")
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		fixture.app.runCtx = hostCtx
		baseTransport := http.DefaultTransport.(*http.Transport).Clone()
		t.Cleanup(baseTransport.CloseIdleConnections)
		fixture.app.legacyCollector.SetHTTPClient(&http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/usage-queue") && failNextDial.CompareAndSwap(true, false) {
					select {
					case errServed <- struct{}{}:
					default:
					}
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
				}
				return baseTransport.RoundTrip(req)
			}),
		})
		var clockMS atomic.Int64
		clockMS.Store(time.Now().UnixMilli())
		fixture.app.legacyCollector.SetNowFunc(clockMS.Load)
		fixture.app.usage.SetClock(func() int64 { return clockMS.Load() + 1000 })

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
		t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok {
			t.Fatal("missing initial collection start")
		}
		budget, hasBudget := readSettingInt64(t, fixture.opened.DB, "fresh_usage_last_retention_ms")
		if !hasBudget || budget != 7_500 {
			t.Fatalf("fresh_usage_last_retention_ms = (%d, %v), want 7500", budget, hasBudget)
		}

		// 5,000ms outage <= 7,500ms keeps complete/observed.
		clockMS.Add(5_000)
		failNextDial.Store(true)
		<-errServed
		deadline := time.Now().Add(3 * time.Second)
		for fixture.app.rawCollectionPhase() != usageapp.PhaseError && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		for fixture.app.rawCollectionPhase() != usageapp.PhaseRunning && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		t1, _ := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if t1 != t0 {
			t.Fatalf("5s outage altered startedMS from %d to %d under 7.5s budget", t0, t1)
		}

		// 9,000ms outage (> 7,500ms budget, though < 45s default) advances marker!
		clockMS.Add(9_000)
		failNextDial.Store(true)
		<-errServed
		var t2 int64
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cur, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && cur > t0 {
				t2 = cur
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if t2 <= t0 {
			t.Fatalf("9s outage > 7.5s budget did not advance startedMS (%d <= %d)", t2, t0)
		}
		waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
	})

	t.Run("non-JSON 200 config fails closed on initial admission backfill", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "text/plain")
					_, _ = io.WriteString(w, `not json`)
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(muxSrv.Close)
			return muxSrv.URL
		}, "source-client-two", "source-client-one")
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		fixture.app.runCtx = hostCtx
		baseClock := time.Now().UnixMilli()
		var clockMS atomic.Int64
		clockMS.Store(baseClock + 500)
		fixture.app.legacyCollector.SetNowFunc(clockMS.Load)
		fixture.app.usage.SetClock(func() int64 { return baseClock + 1000 })

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
		admittedMS, _ := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")
		startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= admittedMS {
			t.Fatalf("unverified retention backfilled startedMS=%d to admittedMS=%d", startedMS, admittedMS)
		}
		budget, _ := readSettingInt64(t, fixture.opened.DB, "fresh_usage_last_retention_ms")
		if budget != 0 {
			t.Fatalf("fresh_usage_last_retention_ms = %d, want 0", budget)
		}
	})
}
func TestUsageSourceIdentityChangeAdvancesCollectionStart(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	var dual *dualProtoCPAServer
	fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
		dual = newDualProtoCPAServer(t, http.HandlerFunc(dest.serve))
		return dual.URL()
	}, "source-client-two", "source-client-one")
	fixture.app.cfg.CollectorMode = "resp"
	fixture.app.cfg.Queue = "usage:a"
	fixture.app.cfg.PollInterval = 15 * time.Millisecond
	fixture.app.runCtx = hostCtx

	if complete := fixture.complete(t); complete.Code != http.StatusOK {
		t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
	}
	waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
	t0, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
	if !ok {
		t.Fatal("missing initial fresh_usage_collection_started_at_ms")
	}
	src0, hasSrc0 := readSettingString(t, fixture.opened.DB, "fresh_usage_last_source_id")
	if !hasSrc0 || src0 == "" {
		t.Fatalf("missing fresh_usage_last_source_id (got %q, %v)", src0, hasSrc0)
	}

	// Change only the RESP queue ("usage:a" -> "usage:b") while keeping the same
	// transport ("resp") and endpoint: SourceID changes so continuity resets cleanly.
	time.Sleep(25 * time.Millisecond)
	fixture.app.cfg.Queue = "usage:b"
	putBody := fmt.Sprintf(`{"config":{"cpaConnection":{"cpaBaseUrl":%q},"collector":{"collectorMode":"resp","queue":"usage:b","pollIntervalMs":15}}}`, dual.URL())
	if resp := gatewayRequest(t, fixture.app, http.MethodPut, "cpamp/usage-service/config", putBody, "application/json"); resp.Code != http.StatusOK {
		t.Fatalf("PUT new queue = %d %s", resp.Code, resp.Body.String())
	}

	var t1 int64
	var src1 string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		curT, curOk := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		curSrc, _ := readSettingString(t, fixture.opened.DB, "fresh_usage_last_source_id")
		if curOk && curT > t0 && curSrc != "" && curSrc != src0 {
			t1 = curT
			src1 = curSrc
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if t1 <= t0 || src1 == "" || src1 == src0 {
		t.Fatalf("source change = (t1=%d <= t0=%d, src1=%q src0=%q)", t1, t0, src1, src0)
	}
	waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
}

func TestInitialRetainedDrainHoldsMarkerAndCorruptedDisableSeqFailsClosed(t *testing.T) {
	t.Run("multi-batch startup drain holds collection marker until queue drains below batchSize", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var pollNum atomic.Int32
		poll1Done := make(chan struct{})
		releasePoll2 := make(chan struct{})
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/usage-queue" && r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					n := pollNum.Add(1)
					switch n {
					case 1:
						_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:01Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"source-client-two","input_tokens":1,"output_tokens":1,"total_tokens":2},{"timestamp":"2026-11-27T00:00:02Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"source-client-two","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
						close(poll1Done)
					case 2:
						<-releasePoll2
						_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:03Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"source-client-two","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
					default:
						_, _ = io.WriteString(w, `[]`)
					}
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(muxSrv.Close)
			return muxSrv.URL
		}, "source-client-two", "source-client-one")
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.BatchSize = 2
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		fixture.app.runCtx = hostCtx

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		select {
		case <-poll1Done:
		case <-time.After(3 * time.Second):
			t.Fatal("poll 1 did not run")
		}
		// Give poll 1 time to finish ingesting its 2 items before checking marker is held.
		time.Sleep(40 * time.Millisecond)
		if startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && startedMS > 0 {
			t.Fatalf("fresh_usage_collection_started_at_ms=%d committed before full batch drained", startedMS)
		}
		if !fixture.app.usageStartupPending.Load() {
			t.Fatal("usageStartupPending cleared while initial full-batch drain was still in progress")
		}

		close(releasePoll2)
		waitUsageSummaryCoverage(t, fixture.app, "complete", "observed")
		if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); !ok {
			t.Fatal("missing fresh_usage_collection_started_at_ms after drain completed")
		}
	})

	t.Run("corrupted monitoring_disable_seq refuses Pop until repaired", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		fixture.app.runCtx = hostCtx
		fixture.runtime.status.State = model.RuntimeStateReady
		wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())

		// Corrupt fresh_usage_monitoring_disable_seq before completion starts Collector.
		if _, err := fixture.opened.DB.ExecContext(t.Context(),
			`insert into settings(key, value, updated_at_ms) values('fresh_usage_monitoring_disable_seq', 'corrupted', 1)
			 on conflict(key) do update set value='corrupted', updated_at_ms=1`); err != nil {
			t.Fatal(err)
		}
		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		time.Sleep(120 * time.Millisecond)
		fixture.destination.usageQueueMu.Lock()
		polls := fixture.destination.usageQueuePolls
		fixture.destination.usageQueueMu.Unlock()
		if polls != 0 {
			t.Fatalf("Pop called %d times with corrupted disableSeq, want 0", polls)
		}
		if startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && startedMS > 0 {
			t.Fatalf("fresh_usage_collection_started_at_ms=%d written with corrupted disableSeq", startedMS)
		}

		// Repair fresh_usage_monitoring_disable_seq -> Collector resumes polling and persists marker.
		if _, err := fixture.opened.DB.ExecContext(t.Context(),
			`update settings set value='0', updated_at_ms=2 where key='fresh_usage_monitoring_disable_seq'`); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		var recoveredMS int64
		for time.Now().Before(deadline) {
			if v, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok && v > 0 {
				recoveredMS = v
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if recoveredMS <= 0 {
			t.Fatal("collector never observed after repairing fresh_usage_monitoring_disable_seq")
		}
	})
}

type faultOnceSettingRepo struct {
	setting.Repository
	failWhen func() bool
	failErr  error
}

func (r *faultOnceSettingRepo) LoadManagerConfig(ctx context.Context) (model.ManagerConfig, bool, error) {
	if r.failWhen != nil && r.failWhen() {
		return model.ManagerConfig{}, false, r.failErr
	}
	return r.Repository.LoadManagerConfig(ctx)
}

func TestPostStartMonitoringSwitchTransientErrorStopsCollectorAndRetriesCleanly(t *testing.T) {
	t.Run("fresh start transient post-start switch error stops run without deadlock and retry observes", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var rejectQueue atomic.Bool
		rejectQueue.Store(true)
		release401 := make(chan struct{})
		var releaseOnce sync.Once
		unblock401 := func() {
			releaseOnce.Do(func() { close(release401) })
		}
		t.Cleanup(unblock401)
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/usage-queue" && rejectQueue.Load() {
					select {
					case <-release401:
					case <-r.Context().Done():
						return
					}
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 5 * time.Millisecond
		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		fixture.app.runCtx = hostCtx
		var failedOnce atomic.Bool
		fixture.app.runtimeSettings = &faultOnceSettingRepo{
			Repository: fixture.app.runtimeSettings,
			failWhen: func() bool {
				if fixture.app.legacyCollector.StartCount() == 1 && !failedOnce.Swap(true) {
					unblock401()
					return true
				}
				return false
			},
			failErr: errors.New("transient settings failure"),
		}
		op, err := fixture.app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		doneFirst := make(chan bool, 1)
		go func() {
			fixture.app.usageStartMu.Lock()
			defer fixture.app.usageStartMu.Unlock()
			doneFirst <- fixture.app.attemptUsageStartWithWarmState(hostCtx, op, false, warmStartUsageState{})
		}()
		select {
		case ok := <-doneFirst:
			if ok {
				t.Fatal("first attemptUsageStartWithWarmState = true, want false on post-start switch error")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("attemptUsageStartWithWarmState deadlocked stopping Collector on post-start switch error")
		}
		if fixture.app.usageCollectionStarted() || fixture.app.usageStartupPending.Load() || fixture.app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("post-failure state: started=%v pending=%v phase=%s", fixture.app.usageCollectionStarted(), fixture.app.usageStartupPending.Load(), fixture.app.collectionPhase())
		}

		rejectQueue.Store(false)
		fixture.app.startUsageCollection(op)
		waitForCondition(t, func() bool {
			v, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
			return ok && v > 0
		})
	})

	t.Run("warm-started collector stops when post-start switch check fails and retry observes", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture := adoptionFixtureWithKeys(t, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 10 * time.Millisecond
		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		fixture.app.runCtx = hostCtx
		fixture.runtime.status.State = model.RuntimeStateReady
		wireAdoptManagedAuthority(fixture.app, fixture.runtime, fixture.destination.URL())
		var failedOnce atomic.Bool
		fixture.app.runtimeSettings = &faultOnceSettingRepo{
			Repository: fixture.app.runtimeSettings,
			failWhen: func() bool {
				return fixture.app.legacyCollector.StartCount() == 1 && !failedOnce.Swap(true)
			},
			failErr: errors.New("transient post-warm-start switch error"),
		}
		op, err := fixture.app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		warm := fixture.app.warmStartUsageCollectorBeforeCompletion(hostCtx, op)
		if !warm.started {
			t.Fatalf("warmStart = %+v, want started=true", warm)
		}
		fixture.app.startUsageCollectionWithWarmState(op, warm)
		if fixture.app.usageCollectionStarted() || fixture.app.usageStartupPending.Load() || fixture.app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("after warm-start post-check failure: started=%v pending=%v phase=%s", fixture.app.usageCollectionStarted(), fixture.app.usageStartupPending.Load(), fixture.app.collectionPhase())
		}
		if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok {
			t.Fatal("fresh_usage_collection_started_at_ms written despite failed post-start switch check")
		}

		fixture.app.startUsageCollection(op)
		waitForCondition(t, func() bool {
			v, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
			return ok && v > 0
		})
	})
}

func TestManagedCPAUsageStatisticsDisabledRefusesStartupAndRecordsGap(t *testing.T) {
	t.Run("warm start during completion refuses statistics disabled and records gap", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		var statsEnabled atomic.Bool
		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					if statsEnabled.Load() {
						_, _ = io.WriteString(w, `{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":60}`)
					} else {
						_, _ = io.WriteString(w, `{"usage-statistics-enabled":false,"redis-usage-queue-retention-seconds":60}`)
					}
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		fixture.app.runCtx = hostCtx

		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		if fixture.app.usageCollectionStarted() || fixture.app.usageStartupPending.Load() || fixture.app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("state with usage-statistics-enabled=false: started=%v pending=%v phase=%s", fixture.app.usageCollectionStarted(), fixture.app.usageStartupPending.Load(), fixture.app.collectionPhase())
		}
		if got := fixture.app.usageStartBlock(); got != usageStartReasonStatisticsDisabled {
			t.Fatalf("usageStartBlock = %q, want %q", got, usageStartReasonStatisticsDisabled)
		}
		if gap, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_unreplayed_gap"); !ok || gap != 1 {
			t.Fatalf("fresh_usage_unreplayed_gap = (%d, %v), want (1, true)", gap, ok)
		}
		if _, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms"); ok {
			t.Fatal("fresh_usage_collection_started_at_ms written while usage-statistics-enabled=false")
		}

		// Re-enabling statistics starts cleanly without backfilling to gatewayAdmittedAtMS because the gap was recorded.
		admittedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_gateway_admitted_at_ms")
		if !ok || admittedMS <= 0 {
			t.Fatalf("fresh_gateway_admitted_at_ms = (%d, %v), want positive", admittedMS, ok)
		}
		time.Sleep(5 * time.Millisecond)
		statsEnabled.Store(true)
		op, err := fixture.app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		fixture.app.startUsageCollection(op)
		waitUsageSummaryCoverage(t, fixture.app, "partial", "window_partial")
		startedMS, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_collection_started_at_ms")
		if !ok || startedMS <= admittedMS {
			t.Fatalf("collectingAt after disabled gap = (%d, %v), want > admittedMS=%d", startedMS, ok, admittedMS)
		}
	})

	t.Run("direct attemptUsageStartWithWarmState refuses statistics disabled and records gap", func(t *testing.T) {
		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()

		fixture, _ := adoptionFixtureWithDestination(t, func(dest *migrationFakeCPA) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"usage-statistics-enabled":false,"redis-usage-queue-retention-seconds":60}`)
					return
				}
				dest.serve(w, r)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}, "source-client-two", "source-client-one")
		fixture.destination.serveUsageAPI = true
		fixture.app.cfg.CollectorMode = "http"
		fixture.app.cfg.PollInterval = 15 * time.Millisecond
		if complete := fixture.complete(t); complete.Code != http.StatusOK {
			t.Fatalf("adopt complete = %d %s", complete.Code, complete.Body.String())
		}
		fixture.app.runCtx = hostCtx
		op, err := fixture.app.current(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		fixture.app.startUsageCollection(op)
		if fixture.app.usageCollectionStarted() || fixture.app.usageStartupPending.Load() || fixture.app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("direct start state with stats disabled: started=%v pending=%v phase=%s", fixture.app.usageCollectionStarted(), fixture.app.usageStartupPending.Load(), fixture.app.collectionPhase())
		}
		if got := fixture.app.usageStartBlock(); got != usageStartReasonStatisticsDisabled {
			t.Fatalf("usageStartBlock = %q, want %q", got, usageStartReasonStatisticsDisabled)
		}
		if gap, ok := readSettingInt64(t, fixture.opened.DB, "fresh_usage_unreplayed_gap"); !ok || gap != 1 {
			t.Fatalf("fresh_usage_unreplayed_gap = (%d, %v), want (1, true)", gap, ok)
		}
	})
}
