package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// externalUsageCPA is the user's own CPA as these tests model it: one management
// credential, one usage-statistics switch, and a usage queue the real Collector
// polls.
type externalUsageCPA struct {
	management atomic.Value
	enabled    atomic.Bool
	rejects    atomic.Bool
	statWrites atomic.Int32
	queueReads atomic.Int32
	// refusals counts the requests this CPA answered 401 because the presented
	// credential was not the current one. It is what proves a stopped Collector
	// sends nothing more and a rebound one sends only the new credential.
	refusals atomic.Int32
	// requests counts every request that reached the CPA, so a test can prove a
	// stopped Collector issues no further traffic at all.
	requests atomic.Int32
	version  atomic.Value
	server   *httptest.Server
}

func newExternalUsageCPA(t *testing.T, management string) *externalUsageCPA {
	t.Helper()
	f := &externalUsageCPA{}
	f.management.Store(management)
	f.version.Store("v8.0.13")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if f.rejects.Load() || r.Header.Get("Authorization") != "Bearer "+f.management.Load().(string) {
			f.refusals.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", f.version.Load().(string))
			_, _ = w.Write([]byte(`{}`))
		case "/v0/management/usage-statistics-enabled":
			if r.Method == http.MethodPut {
				f.statWrites.Add(1)
				f.enabled.Store(true)
			}
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":` + boolText(f.enabled.Load()) + `}`))
		case "/v8/management/config/access/api-keys":
			_, _ = w.Write([]byte("[]"))
		case "/v8/management/config.yaml":
			_, _ = w.Write([]byte(`{"config-version":8}`))
		case "/v0/management/usage-queue":
			f.queueReads.Add(1)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// externalUsageHarness is a routed Fresh application whose committed checkpoint
// and protected connection name one fake External CPA.
type externalUsageHarness struct {
	app *freshApplication
	cpa *externalUsageCPA
	db  *sql.DB
}

func newExternalUsageHarness(t *testing.T) *externalUsageHarness {
	t.Helper()
	hostCtx, stopHost := context.WithCancel(t.Context())
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(hostCtx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		stopHost()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopHost()
		_ = opened.Close()
	})
	// The Collector polls over HTTP in these tests; RESP discovery would only add
	// an unrelated TCP exchange to the fake CPA.
	app := newFreshApplication(opened, config.Config{CollectorMode: "http"}, nil)
	app.runCtx = hostCtx
	quietProjection(app)

	fake := newExternalUsageCPA(t, externalAuthorityManagement)
	normalized, err := cpa.NormalizeV8BaseURL(fake.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := opened.Protector.ProtectString(externalAuthorityManagement)
	if err != nil {
		t.Fatal(err)
	}
	op := opened.Record.Operation
	if _, err = app.store.Update(hostCtx, op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(normalized), TargetGeneration: 1,
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: normalized, ManagementKey: protected}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.externalSource = externalInstallSource(app.store, opened.Protector)
	app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)
	return &externalUsageHarness{app: app, cpa: fake, db: opened.DB}
}

func (h *externalUsageHarness) call(t *testing.T, method, resource, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	h.app.canonicalAPI(response, request)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s weakened API cache control", resource)
	}
	return response
}

func (h *externalUsageHarness) collectionStartRows(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`select count(*) from settings where key = 'fresh_usage_collection_started_at_ms'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (h *externalUsageHarness) bindingIdentity(t *testing.T, id freshkeys.Key) string {
	t.Helper()
	var identity string
	if err := h.db.QueryRow(`select runtime_identity from gateway_api_key_source_bindings where api_key_id = ?`, string(id.ID)).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	return identity
}

func (h *externalUsageHarness) operation(t *testing.T) bootstrap.FreshOperation {
	t.Helper()
	record, err := h.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return record.Operation
}

func decodeExternalUsageState(t *testing.T, body []byte) externalUsageState {
	t.Helper()
	var state externalUsageState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode usage state: %v (%s)", err, string(body))
	}
	return state
}

// TestExternalUsageCollectionNeedsTheAdministratorConfirmation proves the manager
// never enables usage statistics on a CPA it does not own without being told to,
// and that the confirmed write is proven by reading the switch back.
func TestExternalUsageCollectionNeedsTheAdministratorConfirmation(t *testing.T) {
	h := newExternalUsageHarness(t)

	h.app.startUsageCollection(h.operation(t))
	if h.cpa.statWrites.Load() != 0 {
		t.Fatal("usage statistics were enabled without the administrator's confirmation")
	}
	if phase := h.app.collectionPhase(); phase != usageapp.PhaseStopped {
		t.Fatalf("collector = %s, want stopped without a confirmed switch", phase)
	}
	if reason := h.app.usageStartBlock(); reason != usageStartReasonStatisticsDisabled {
		t.Fatalf("blocked reason = %q", reason)
	}
	if rows := h.collectionStartRows(t); rows != 0 {
		t.Fatal("a refused start recorded a coverage window")
	}

	// The confirmation is the only path that writes the switch.
	response := h.call(t, http.MethodPost, "external/usage",
		`{"requestId":"`+strings.Repeat("a", 32)+`","confirmEnableStatistics":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", response.Code, response.Body.String())
	}
	if writes := h.cpa.statWrites.Load(); writes != 1 {
		t.Fatalf("statistics writes = %d, want exactly one", writes)
	}
	if rows := h.collectionStartRows(t); rows != 1 {
		t.Fatalf("coverage windows = %d, want one", rows)
	}
	state := decodeExternalUsageState(t, response.Body.Bytes())
	if !state.StatisticsEnabled || !state.CollectionStarted || state.BlockedReason != "" {
		t.Fatalf("state = %+v", state)
	}
	// The state read never carries a credential.
	if strings.Contains(response.Body.String(), externalAuthorityManagement) {
		t.Fatal("the usage state leaked the management credential")
	}
}

// TestExternalUsageCollectionRejectsAnUnconfirmedRequest proves a POST that does
// not confirm anything writes nothing at all.
func TestExternalUsageCollectionRejectsAnUnconfirmedRequest(t *testing.T) {
	h := newExternalUsageHarness(t)
	for _, body := range []string{
		`{"requestId":"` + strings.Repeat("a", 32) + `"}`,
		`{"requestId":"` + strings.Repeat("a", 32) + `","confirmEnableStatistics":false}`,
		`{"confirmEnableStatistics":true}`,
		`{"requestId":"` + strings.Repeat("a", 32) + `","confirmEnableStatistics":true,"extra":1}`,
	} {
		if response := h.call(t, http.MethodPost, "external/usage", body); response.Code != http.StatusBadRequest {
			t.Fatalf("unconfirmed %s = %d", body, response.Code)
		}
	}
	if writes := h.cpa.statWrites.Load(); writes != 0 {
		t.Fatalf("statistics writes = %d, want none", writes)
	}
	if rows := h.collectionStartRows(t); rows != 0 {
		t.Fatal("an unconfirmed request recorded a coverage window")
	}
}

// TestExternalUsageCollectionBindsTheConnectionIdentity proves staged keys are
// attributed to external:sha256:<digest>, never to a raw address and never to
// the managed destination, so history cannot follow the connection elsewhere.
func TestExternalUsageCollectionBindsTheConnectionIdentity(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	staged := stageClientKey(t, h.app)
	identity := h.operation(t).Strategy.TargetIdentity
	if !strings.HasPrefix(identity, "external:sha256:") {
		t.Fatalf("committed identity = %q", identity)
	}

	h.app.startUsageCollection(h.operation(t))
	if phase := h.app.collectionPhase(); phase == usageapp.PhaseStopped {
		t.Fatalf("collector = %s after an already enabled switch", phase)
	}
	if bound := h.bindingIdentity(t, staged.Key); bound != identity {
		t.Fatalf("binding identity = %q, want the connection digest %q", bound, identity)
	}
	if rows := h.collectionStartRows(t); rows != 1 {
		t.Fatalf("coverage windows = %d, want one", rows)
	}
	// A later attempt is a replay: no second Collector, no second binding.
	h.app.startUsageCollection(h.operation(t))
	if bound := h.bindingIdentity(t, staged.Key); bound != identity {
		t.Fatalf("replay moved the binding to %q", bound)
	}
}

// TestExternalUsageCollectionStopsOnItsOwnRejectedCredential rotates the
// management credential while the real Collector is polling. The Collector must
// stop itself on the first refusal — a CPA bans an address after a handful of
// failed authentications, so retrying is what turns a rotation into an outage —
// clear the started latch, and record the blocker. A rebind then restarts
// collection on the new credential, and the replaced one is never sent again.
func TestExternalUsageCollectionStopsOnItsOwnRejectedCredential(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	h.app.startUsageCollection(h.operation(t))
	if !h.app.usageCollectionStarted() {
		t.Fatal("collection did not start on a CPA that accepts the credential")
	}
	waitForCondition(t, func() bool { return h.cpa.queueReads.Load() > 0 })

	// The user rotates the management credential on their own CPA.
	rotated := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	h.cpa.management.Store(rotated)
	waitForCondition(t, func() bool { return h.app.usageStartBlock() != "" })

	// The Collector stopped itself, the started latch is gone, and the blocker is
	// the typed rejection rather than an unexplained stopped Collector.
	if phase := h.app.collectionPhase(); phase != usageapp.PhaseStopped {
		t.Fatalf("collector = %s after a refusal, want stopped", phase)
	}
	if h.app.usageCollectionStarted() {
		t.Fatal("a refused Collector still reported coverage")
	}
	if reason := h.app.usageStartBlock(); reason != freshconfig.ReasonManagementRejected {
		t.Fatalf("blocked reason = %q, want %q", reason, freshconfig.ReasonManagementRejected)
	}
	// No further request follows: the refusals stop at the ones already made.
	refusals := h.cpa.refusals.Load()
	requests := h.cpa.requests.Load()
	if refusals > 3 {
		t.Fatalf("the Collector retried a refused credential %d times", refusals)
	}
	waitForCondition(t, func() bool {
		// Nothing may arrive after the Collector reported: give an unbounded
		// retry loop the chance to prove itself wrong.
		return h.cpa.requests.Load() > requests
	}, false)
	if h.cpa.requests.Load() != requests {
		t.Fatalf("a stopped Collector sent %d more requests", h.cpa.requests.Load()-requests)
	}

	// The administrator rebinds the connection with the rotated credential, and
	// collection restarts on it: the replaced credential is never sent again.
	response := h.call(t, http.MethodPost, "external/rebind", rebindBody("", rotated))
	if response.Code != http.StatusOK {
		t.Fatalf("rebind = %d %s", response.Code, response.Body.String())
	}
	if !h.app.usageCollectionStarted() {
		t.Fatalf("collection did not restart after the rebind (blocked: %q)", h.app.usageStartBlock())
	}
	afterRebind := h.cpa.refusals.Load()
	queueReads := h.cpa.queueReads.Load()
	waitForCondition(t, func() bool { return h.cpa.queueReads.Load() > queueReads })
	if h.cpa.refusals.Load() != afterRebind {
		t.Fatalf("the replaced credential was sent again %d times", h.cpa.refusals.Load()-afterRebind)
	}
	state := decodeExternalUsageState(t, h.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
	if !state.CollectionStarted || state.BlockedReason != "" {
		t.Fatalf("state after the rebind = %+v", state)
	}
}

// waitForCondition polls a condition with a bounded budget. With want=false it
// returns as soon as the budget expires, which is how a test proves a loop does
// not run at all.
func waitForCondition(t *testing.T, condition func() bool, want ...bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ok := condition()
		if len(want) > 0 && !want[0] {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition was not met within the budget")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestExternalUsageConfirmationIsConsumedByOneAttempt proves the confirmation
// authorises one enable attempt and nothing more. A replay of the same
// completion must not write the switch again on its own, and a confirm while
// collection is already live must neither restart the Collector nor clear the
// started latch — the running Collector is the same one throughout.
func TestExternalUsageConfirmationIsConsumedByOneAttempt(t *testing.T) {
	h := newExternalUsageHarness(t)
	body := `{"requestId":"` + strings.Repeat("a", 32) + `","confirmEnableStatistics":true}`
	if response := h.call(t, http.MethodPost, "external/usage", body); response.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", response.Code, response.Body.String())
	}
	if writes := h.cpa.statWrites.Load(); writes != 1 {
		t.Fatalf("statistics writes = %d, want exactly one", writes)
	}
	queueReads := h.cpa.queueReads.Load()
	waitForCondition(t, func() bool { return h.cpa.queueReads.Load() > queueReads })

	// The user turns usage statistics off on their own CPA. A replay of the
	// completion must not turn it back on: the approval it used is spent.
	h.cpa.enabled.Store(false)
	h.app.startUsageCollection(h.operation(t))
	if writes := h.cpa.statWrites.Load(); writes != 1 {
		t.Fatalf("a replay wrote the switch again: %d writes", writes)
	}

	// A confirm while collection is live is a no-op: the latch survives and the
	// Collector is not restarted.
	if !h.app.usageCollectionStarted() {
		t.Fatal("collection is not live, so this case would not be proven")
	}
	// The Collector's identity is its start count, not its phase: a restart is
	// observable as "starting" only for a moment, so a phase comparison passes
	// whether or not one happened.
	beforeCollector := h.app.legacyCollector.StartCount()
	if beforeCollector == 0 {
		t.Fatal("no Collector was started, so this case would not be proven")
	}
	response := h.call(t, http.MethodPost, "external/usage", body)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm while live = %d %s", response.Code, response.Body.String())
	}
	state := decodeExternalUsageState(t, response.Body.Bytes())
	if !state.CollectionStarted || state.BlockedReason != "" {
		t.Fatalf("a confirm while live changed the state: %+v", state)
	}
	if after := h.app.legacyCollector.StartCount(); after != beforeCollector {
		t.Fatalf("a confirm while live restarted the Collector: %d starts, was %d", after, beforeCollector)
	}
	if writes := h.cpa.statWrites.Load(); writes != 1 {
		t.Fatalf("a confirm while live wrote the switch: %d writes", writes)
	}
}

// TestExternalUsageCollectionStopsOnARejectedCredential proves a 401 stops
// collection and surfaces the typed blocker instead of retrying.
func TestExternalUsageCollectionStopsOnARejectedCredential(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	h.app.startUsageCollection(h.operation(t))
	if phase := h.app.collectionPhase(); phase == usageapp.PhaseStopped {
		t.Fatalf("collector = %s before the credential was rotated", phase)
	}

	h.cpa.rejects.Store(true)
	outcome := h.app.startExternalUsageCollector(t.Context())
	if outcome.Started || outcome.Reason != freshconfig.ReasonManagementRejected {
		t.Fatalf("outcome = %+v, want the typed rejection", outcome)
	}
	if phase := h.app.collectionPhase(); phase != usageapp.PhaseStopped {
		t.Fatalf("collector = %s after a 401, want stopped", phase)
	}
	response := h.call(t, http.MethodGet, "external/usage", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("usage state after a 401 = %d %s", response.Code, response.Body.String())
	}
}

// TestExternalUsageCollectionClaimsNothingWhenNothingStarts covers the defect the
// plan names: MarkCollectionStart must only follow a real start.
func TestExternalUsageCollectionClaimsNothingWhenNothingStarts(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.app.externalSource = nil
	h.app.managedSource = nil
	h.app.privateCollector = nil

	h.app.startUsageCollection(h.operation(t))
	if rows := h.collectionStartRows(t); rows != 0 {
		t.Fatal("a strategy with no Collector authority recorded a coverage window")
	}
	if phase := h.app.collectionPhase(); phase != usageapp.PhaseStopped {
		t.Fatalf("collector = %s, want stopped", phase)
	}
	if reason := h.app.usageStartBlock(); reason != usageStartReasonNoAuthority {
		t.Fatalf("blocked reason = %q, want %q", reason, usageStartReasonNoAuthority)
	}
}

// TestExternalUsageStateNeverClaimsAnAttemptedStart proves the reported
// CollectionStarted is the real started latch, not the absence of a refusal: a
// Collector that was simply never attempted has no blocker, and must still read
// as no coverage.
func TestExternalUsageStateNeverClaimsAnAttemptedStart(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)

	response := h.call(t, http.MethodGet, "external/usage", "")
	if response.Code != http.StatusOK {
		t.Fatalf("usage state = %d %s", response.Code, response.Body.String())
	}
	state := decodeExternalUsageState(t, response.Body.Bytes())
	if state.CollectionStarted || state.BlockedReason != "" {
		t.Fatalf("state = %+v, want no coverage and no blocker", state)
	}

	h.app.startUsageCollection(h.operation(t))
	if !h.app.usageCollectionStarted() {
		t.Fatal("a successful start did not latch collection")
	}
	response = h.call(t, http.MethodGet, "external/usage", "")
	if state = decodeExternalUsageState(t, response.Body.Bytes()); !state.CollectionStarted {
		t.Fatalf("state = %+v, want the started latch", state)
	}
}

// TestExternalUsageCollectionRefusesAForeignConnection proves an authority that
// no longer names the committed CPA starts nothing and keeps the first binding:
// a rebind to another CPA must never inherit the previous history.
func TestExternalUsageCollectionRefusesAForeignConnection(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	staged := stageClientKey(t, h.app)
	firstIdentity := h.operation(t).Strategy.TargetIdentity
	h.app.startUsageCollection(h.operation(t))

	// The checkpoint and the live connection disagree: the authority refuses,
	// so no Collector and no new attribution window exist.
	record, err := h.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy.TargetIdentity = "external:sha256:" + strings.Repeat("c", 64)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.app.resetUsageStart()
	outcome := h.app.startExternalUsageCollector(t.Context())
	if outcome.Started || outcome.Reason != usageStartReasonExternalUnavailable {
		t.Fatalf("outcome = %+v", outcome)
	}
	if bound := h.bindingIdentity(t, staged.Key); bound != firstIdentity {
		t.Fatalf("binding moved to %q, want the original %q", bound, firstIdentity)
	}
}

// TestExternalUsageStateRefusesAnIncompleteInstall proves the workspace answers a
// state conflict instead of a quiet empty posture before completion.
func TestExternalUsageStateRefusesAnIncompleteInstall(t *testing.T) {
	h := newExternalUsageHarness(t)
	op := h.operation(t)
	op.ProductComplete = false
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := httptest.NewRequest(method, "/control/api/v2/external/usage",
			bytes.NewBufferString(`{"requestId":"`+strings.Repeat("a", 32)+`","confirmEnableStatistics":true}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.app.externalUsageRoute(response, request, op, "")
		if response.Code != http.StatusConflict || response.Body.Len() != 0 {
			t.Fatalf("%s before completion = %d %s", method, response.Code, response.Body.String())
		}
	}
	if writes := h.cpa.statWrites.Load(); writes != 0 {
		t.Fatalf("statistics writes = %d, want none", writes)
	}
}

// TestManagedUsageStartRecordsCollectionOnlyAfterAuthenticating proves the
// managed profile gained the same guarantee: a Collector whose credential does
// not answer an authenticated read records no coverage window.
func TestManagedUsageStartRecordsCollectionOnlyAfterAuthenticating(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.app.externalSource = nil
	h.app.keyApply = nil
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer managed.Close()
	h.app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		op := h.operation(t)
		return freshkeyapply.ManagedInstall{
			RunID: "managed-run", Generation: "1", Identity: op.Strategy.TargetIdentity,
			BaseURL: managed.URL, ManagementKey: strings.Repeat("a", 64),
		}, nil
	}
	h.app.startUsageCollection(h.operation(t))
	if rows := h.collectionStartRows(t); rows != 0 {
		t.Fatal("the managed profile claimed collection without an authenticated sample")
	}
}

// TestExternalRejectionStopsBrowserDrivenReads proves a refused credential is not
// presented again by the pages that refresh. CPA bans the calling address for
// thirty minutes after five failed authentications, so a browser polling this
// Manager would reach that ban on its own: once the refusal is recorded, every
// browser-driven read answers the typed blocker locally and sends nothing.
func TestExternalRejectionStopsBrowserDrivenReads(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.rejects.Store(true)

	// The first read is the one that learns the refusal.
	first := h.call(t, http.MethodGet, "external/usage", "")
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("first read = %d %s", first.Code, first.Body.String())
	}
	if !h.app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal was not recorded, so the later reads would send the key again")
	}

	requests := h.cpa.requests.Load()
	if requests == 0 {
		t.Fatal("the first read never reached the CPA, so nothing was proven")
	}
	for i := 0; i < 4; i++ {
		for _, resource := range []string{"external/usage", "api-keys", "gateway/config"} {
			response := h.call(t, http.MethodGet, resource, "")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
				t.Fatalf("%s after the refusal = %d %s", resource, response.Code, response.Body.String())
			}
		}
	}
	if sent := h.cpa.requests.Load() - requests; sent != 0 {
		t.Fatalf("a refused credential was presented %d more times", sent)
	}
}

// TestExternalRebindReverifiesTheStoredCredential proves the way back when the key
// never changed. The usual cause of a rejected credential is a ban window that has
// since passed, so re-supplying the same key must re-verify it, clear the blocker
// and restart collection — without claiming a rotation that never happened.
func TestExternalRebindReverifiesTheStoredCredential(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	h.cpa.rejects.Store(true)
	if response := h.call(t, http.MethodGet, "external/usage", ""); response.Code != http.StatusConflict {
		t.Fatalf("read on a refusing CPA = %d", response.Code)
	}
	before, err := h.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	starts := h.app.legacyCollector.StartCount()

	// The ban window passes; the key is unchanged.
	h.cpa.rejects.Store(false)
	response := h.call(t, http.MethodPost, "external/rebind", rebindBody("", externalAuthorityManagement))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "reverified") {
		t.Fatalf("same-key rebind = %d %s", response.Code, response.Body.String())
	}
	after, err := h.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Operation.Revision != before.Operation.Revision {
		t.Fatalf("a re-verify bumped the revision from %d to %d", before.Operation.Revision, after.Operation.Revision)
	}
	if after.Connection.ManagementKey != before.Connection.ManagementKey {
		t.Fatal("a re-verify rewrote the stored credential")
	}
	if h.app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal survived a proven credential")
	}
	if h.app.legacyCollector.StartCount() <= starts {
		t.Fatal("collection was not restarted on the credential that now authenticates")
	}
	if state := decodeExternalUsageState(t, h.call(t, http.MethodGet, "external/usage", "").Body.Bytes()); state.BlockedReason != "" {
		t.Fatalf("the blocker survived the re-verify: %+v", state)
	}
}

// TestExternalRejectionStopsThePollingReads covers the two reads that poll a
// journaled key step. They are the busiest of all — the page reloads them while a
// step settles — so an ungated poll is the fastest route to the ban that CPA
// imposes after five failed authentications.
func TestExternalRejectionStopsThePollingReads(t *testing.T) {
	h := newExternalUsageHarness(t)
	id, _ := h.stagedKey(t)
	h.cpa.rejects.Store(true)

	// The first poll is what learns the refusal.
	first := h.call(t, http.MethodGet, "api-keys/operations/"+strings.Repeat("9", 32), "")
	if first.Code == http.StatusConflict && strings.Contains(first.Body.String(), freshconfig.ReasonManagementRejected) {
		if !h.app.externalReadGrant(t.Context()).Refused {
			t.Fatal("the operation poll learned a refusal but did not record it")
		}
	} else {
		// A journal that does not know this id answers without contacting CPA, so
		// the refusal has to come from the observation read instead.
		observation := h.call(t, http.MethodGet, "api-keys/"+id+"/initial-install", "")
		if observation.Code != http.StatusConflict || !strings.Contains(observation.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Skipf("neither poll reached the CPA: %d %s", observation.Code, observation.Body.String())
		}
	}

	requests := h.cpa.requests.Load()
	for i := 0; i < 3; i++ {
		for _, resource := range []string{
			"api-keys/operations/" + strings.Repeat("9", 32),
			"api-keys/" + id + "/initial-install",
		} {
			response := h.call(t, http.MethodGet, resource, "")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
				t.Fatalf("%s after the refusal = %d %s", resource, response.Code, response.Body.String())
			}
		}
	}
	if sent := h.cpa.requests.Load() - requests; sent != 0 {
		t.Fatalf("a refused credential was presented %d more times", sent)
	}
}

// stagedKey creates one manager-owned key for the read routes that need an id.
func (h *externalUsageHarness) stagedKey(t *testing.T) (string, string) {
	t.Helper()
	result, _, err := h.app.keys.Create(t.Context(), strings.Repeat("b", 32))
	if err != nil {
		t.Fatalf("stage key: %v", err)
	}
	return string(result.Key.ID), result.CreatedSecret
}

// TestExternalRejectionBelongsToTheKeyThatSentIt is the rebind race. A read
// resolves the credential, sends it and is still in flight when the administrator
// rebinds; the answer then arrives, refusing the key that is no longer stored. If
// the refusal were filed against whatever the record holds now, the fresh
// credential would be gated before it was ever sent — the operator would have
// fixed the rotation and still see the blocker.
func TestExternalRejectionBelongsToTheKeyThatSentIt(t *testing.T) {
	h := newExternalUsageHarness(t)
	// The read resolves the old credential and would present it.
	grant := h.app.externalReadGrant(t.Context())
	if grant.Credential == "" {
		t.Fatal("the grant did not capture the stored envelope")
	}
	// The administrator rebinds to a credential the CPA now holds.
	rotated := strings.Repeat("7", 64)
	h.cpa.management.Store(rotated)
	if response := h.call(t, http.MethodPost, "external/rebind", rebindBody("", rotated)); response.Code != http.StatusOK {
		t.Fatalf("rebind = %d %s", response.Code, response.Body.String())
	}
	// The in-flight read answers now, refusing the credential it was sent with.
	h.app.noteExternalRejection(t.Context(), grant, cpa.ErrManagementRejected)
	if h.app.externalReadGrant(t.Context()).Refused {
		t.Fatal("a refusal of the replaced key gated the rebound one")
	}
	if response := h.call(t, http.MethodGet, "external/usage", ""); response.Code != http.StatusOK {
		t.Fatalf("the rebound key was gated: %d %s", response.Code, response.Body.String())
	}
}
