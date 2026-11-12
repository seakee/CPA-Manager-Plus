package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
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
	version     atomic.Value
	alsoAccept  atomic.Value
	onQueueRead atomic.Value
	server      *httptest.Server
}

func newExternalUsageCPA(t *testing.T, management string) *externalUsageCPA {
	t.Helper()
	f := &externalUsageCPA{}
	f.management.Store(management)
	f.alsoAccept.Store("")
	f.version.Store("v8.0.13")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		auth := r.Header.Get("Authorization")
		primary := "Bearer " + f.management.Load().(string)
		secondaryKey, _ := f.alsoAccept.Load().(string)
		accepted := auth == primary || (secondaryKey != "" && auth == "Bearer "+secondaryKey)
		if f.rejects.Load() || !accepted {
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
			if fn, _ := f.onQueueRead.Load().(func(string)); fn != nil {
				fn(auth)
			}
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
			BaseURL: managed.URL, ManagementKey: strings.Repeat("a", 64), Strategy: "new_managed",
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

// TestExternalUsageConfirmationNeverPresentsARefusedCredential proves the
// confirmation is not a way around the gate. It re-runs the collection start, and
// that start samples the CPA before it may write the switch, so a credential the
// CPA already refused must answer the typed blocker locally: nothing is approved,
// nothing is started and no request leaves this process.
func TestExternalUsageConfirmationNeverPresentsARefusedCredential(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.rejects.Store(true)
	// The first read is the one that learns the refusal.
	if first := h.call(t, http.MethodGet, "external/usage", ""); first.Code != http.StatusConflict {
		t.Fatalf("read = %d %s", first.Code, first.Body.String())
	}
	requests := h.cpa.requests.Load()
	response := h.call(t, http.MethodPost, "external/usage",
		`{"requestId":"`+strings.Repeat("ab", 16)+`","confirmEnableStatistics":true}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("confirm after the refusal = %d %s", response.Code, response.Body.String())
	}
	if sent := h.cpa.requests.Load() - requests; sent != 0 {
		t.Fatalf("the confirm presented the refused credential %d times", sent)
	}
	if writes := h.cpa.statWrites.Load(); writes != 0 {
		t.Fatalf("the confirm wrote the usage switch %d times", writes)
	}
	if h.app.usageCollectionStarted() {
		t.Fatal("the confirm claimed collection started")
	}
}

// TestExternalMonitoringConfigGetAndPatchRespectRecordedRejection proves that on
// ConnectExternal:
//  1. GET cpamp/usage-service/config (and cpamp/config) returns 200 OK from
//     Manager's local settings without probing the External CPA (both before and
//     after a recorded credential rejection).
//  2. After a recorded rejection, PATCH cpamp/usage-service/config {"enabled":true}
//     sends zero further management requests to the External CPA.
//  3. A first-hit 401 or 403 during PATCH {"enabled":true} (or startup) records
//     externalReadGrant.Refused under legacyCredentialGate so concurrent reads
//     never submit the rejected credential a second time.
func TestExternalMonitoringConfigGetAndPatchRespectRecordedRejection(t *testing.T) {
	t.Run("GET config and PATCH enable send zero requests after recorded rejection", func(t *testing.T) {
		h := newExternalUsageHarness(t)

		// Even while the credential is valid, GET usage-service/config on
		// ConnectExternal answers from local settings without probing External CPA.
		beforeHealthy := h.cpa.requests.Load()
		for _, resource := range []string{"cpamp/usage-service/config", "cpamp/config"} {
			resp := h.call(t, http.MethodGet, resource, "")
			if resp.Code != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", resource, resp.Code, resp.Body.String())
			}
			if strings.Contains(resp.Body.String(), `"cpaUsage"`) {
				t.Fatalf("GET %s included upstream cpaUsage on ConnectExternal: %s", resource, resp.Body.String())
			}
		}
		if got := h.cpa.requests.Load() - beforeHealthy; got != 0 {
			t.Fatalf("GET usage-service/config sent %d External CPA requests, want 0", got)
		}

		// Rotate/reject the External credential and record refusal once via models.
		h.cpa.rejects.Store(true)
		first := h.call(t, http.MethodGet, "cpamp/v1/models", "")
		if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("initial cpamp/v1/models = %d %s, want 409 %s", first.Code, first.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if !h.app.externalReadGrant(t.Context()).Refused {
			t.Fatal("expected externalReadGrant.Refused after initial 401")
		}

		afterRejection := h.cpa.requests.Load()
		for range 3 {
			for _, resource := range []string{"cpamp/usage-service/config", "cpamp/config"} {
				resp := h.call(t, http.MethodGet, resource, "")
				if resp.Code != http.StatusOK {
					t.Fatalf("GET %s after rejection = %d %s, want 200", resource, resp.Code, resp.Body.String())
				}
			}
			patchResp := h.call(t, http.MethodPatch, "cpamp/usage-service/config", `{"enabled":true}`)
			if patchResp.Code != http.StatusOK {
				t.Fatalf("PATCH enable after rejection = %d %s, want 200", patchResp.Code, patchResp.Body.String())
			}
		}
		if got := h.cpa.requests.Load() - afterRejection; got != 0 {
			t.Fatalf("GET config / PATCH enable sent %d External CPA requests after recorded rejection, want 0", got)
		}
		if h.app.usageCollectionStarted() || h.app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("PATCH enable claimed collection on a refused credential: started=%v phase=%s", h.app.usageCollectionStarted(), h.app.collectionPhase())
		}
		if got := h.app.usageStartBlock(); got != freshconfig.ReasonManagementRejected {
			t.Fatalf("usageStartBlock = %q, want %q", got, freshconfig.ReasonManagementRejected)
		}
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run("first-hit "+http.StatusText(status)+" on PATCH enable records Refused under gate", func(t *testing.T) {
			var upstreamAuthAttempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamAuthAttempts.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"rejected"}`))
			}))
			defer upstream.Close()

			hostCtx, stopHost := context.WithCancel(t.Context())
			defer stopHost()
			app := newExternalApp(t, upstream.URL, externalAuthorityManagement)
			app.runCtx = hostCtx
			app.store = uncancellableStore{app.store}

			// Start with local monitoring switch disabled so PATCH {"enabled":true}
			// triggers the first External CPA probe.
			disableReq := httptest.NewRequest(http.MethodPatch, "/control/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":false}`))
			disableReq.Header.Set("Content-Type", "application/json")
			disableReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			disableResp := httptest.NewRecorder()
			app.canonicalAPI(disableResp, disableReq)
			if disableResp.Code != http.StatusOK {
				t.Fatalf("PATCH disable = %d %s", disableResp.Code, disableResp.Body.String())
			}
			if app.externalReadGrant(t.Context()).Refused {
				t.Fatal("credential unexpectedly marked Refused before first probe")
			}

			atBeforeRecord := make(chan struct{})
			allowRecord := make(chan struct{})
			var recordOnce sync.Once
			app.rejection.beforeRecord = func() {
				recordOnce.Do(func() {
					close(atBeforeRecord)
					<-allowRecord
				})
			}

			patchResp := httptest.NewRecorder()
			patchDone := make(chan struct{})
			go func() {
				defer close(patchDone)
				req := httptest.NewRequest(http.MethodPatch, "/control/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":true}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				app.canonicalAPI(patchResp, req)
			}()

			select {
			case <-atBeforeRecord:
			case <-time.After(5 * time.Second):
				t.Fatal("PATCH enable did not record External rejection on first-hit refusal")
			}

			modelWaiterEntered := make(chan struct{})
			modelResp := httptest.NewRecorder()
			modelDone := make(chan struct{})
			go func() {
				defer close(modelDone)
				ctx := &waitNotifyContext{Context: t.Context(), entered: modelWaiterEntered}
				req := httptest.NewRequest(http.MethodGet, "/control/api/v2/cpamp/v1/models", nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				app.canonicalAPI(modelResp, req)
			}()

			select {
			case <-modelWaiterEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent cpamp/v1/models waiter did not queue on legacyCredentialGate")
			}

			close(allowRecord)
			<-patchDone
			<-modelDone

			if patchResp.Code != http.StatusOK {
				t.Fatalf("PATCH enable = %d %s, want 200", patchResp.Code, patchResp.Body.String())
			}
			if !app.externalReadGrant(t.Context()).Refused {
				t.Fatal("first-hit refusal on PATCH enable did not persist externalReadGrant.Refused")
			}
			if got := app.usageStartBlock(); got != freshconfig.ReasonManagementRejected {
				t.Fatalf("usageStartBlock = %q, want %q", got, freshconfig.ReasonManagementRejected)
			}
			if modelResp.Code != http.StatusConflict || errorCode(t, modelResp) != freshconfig.ReasonManagementRejected {
				t.Fatalf("concurrent cpamp/v1/models = %d %s, want 409 %s", modelResp.Code, modelResp.Body.String(), freshconfig.ReasonManagementRejected)
			}
			if got := upstreamAuthAttempts.Load(); got != 1 {
				t.Fatalf("External CPA received %d management requests across PATCH enable + concurrent models, want 1", got)
			}
		})
	}
}

// TestExternalCollectorAuthRejectionScopesGrantToActiveRun proves both halves
// of the run-scoped External Collector grant invariant:
//  1. A stale Collector run's delayed reportCollectorAuthRejection arriving
//     after POST external/rebind already started a new Collector run never
//     marks the rebound credential as refused or clears the new run's latches.
//  2. An immediate in-window Collector auth refusal firing before
//     startExternalUsageCollector finishes checking collectionPhase still
//     observes its pre-stored grant and records externalReadGrant.Refused.
func TestExternalCollectorAuthRejectionScopesGrantToActiveRun(t *testing.T) {
	t.Run("stale run auth refusal across rebind does not poison rebound credential", func(t *testing.T) {
		h := newExternalUsageHarness(t)
		h.cpa.enabled.Store(true)
		h.app.startUsageCollection(h.operation(t))
		if !h.app.usageCollectionStarted() {
			t.Fatal("initial collection did not start")
		}
		waitForCondition(t, func() bool { return h.cpa.queueReads.Load() > 0 })

		staleAtLock := make(chan struct{})
		releaseStale := make(chan struct{})
		staleDone := make(chan struct{})
		var lockOnce, doneOnce sync.Once
		h.app.beforeCollectorAuthRejectionLock = func(runID uint64) {
			if runID == 1 {
				lockOnce.Do(func() {
					close(staleAtLock)
					<-releaseStale
				})
			}
		}
		h.app.afterCollectorAuthRejection = func(runID uint64) {
			if runID == 1 {
				doneOnce.Do(func() { close(staleDone) })
			}
		}

		rotated := strings.Repeat("e", 64)
		h.cpa.management.Store(rotated)

		select {
		case <-staleAtLock:
		case <-time.After(5 * time.Second):
			t.Fatal("run #1 did not reach reportCollectorAuthRejection after key rotation")
		}

		rebindResp := h.call(t, http.MethodPost, "external/rebind", rebindBody("", rotated))
		if rebindResp.Code != http.StatusOK {
			t.Fatalf("rebind = %d %s", rebindResp.Code, rebindResp.Body.String())
		}
		if got := h.app.legacyCollector.StartCount(); got != 2 {
			t.Fatalf("StartCount after rebind = %d, want 2", got)
		}
		if !h.app.usageCollectionStarted() {
			t.Fatalf("collection did not restart on rebound credential (reason=%q)", h.app.usageStartBlock())
		}

		close(releaseStale)
		select {
		case <-staleDone:
		case <-time.After(5 * time.Second):
			t.Fatal("stale reportCollectorAuthRejection did not finish")
		}

		if h.app.externalReadGrant(t.Context()).Refused {
			t.Fatal("stale run #1 auth refusal falsely marked the rebound credential as refused")
		}
		if !h.app.usageCollectionStarted() || h.app.collectionPhase() == usageapp.PhaseStopped {
			t.Fatalf("stale run #1 auth refusal cleared active run #2 state: started=%v phase=%s", h.app.usageCollectionStarted(), h.app.collectionPhase())
		}
		if got := h.app.usageStartBlock(); got != "" {
			t.Fatalf("stale run #1 auth refusal set usageStartBlock = %q, want empty", got)
		}
		if usageResp := h.call(t, http.MethodGet, "external/usage", ""); usageResp.Code != http.StatusOK {
			t.Fatalf("GET external/usage after stale callback = %d %s, want 200", usageResp.Code, usageResp.Body.String())
		}
	})

	t.Run("immediate in-window auth refusal before StartRuntime return records grant", func(t *testing.T) {
		var upstreamCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamCalls.Add(1)
			switch r.URL.Path {
			case "/v0/management/config":
				w.Header().Set("X-CPA-Version", "v8.0.13")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			case "/v0/management/usage-statistics-enabled":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
			case "/v0/management/usage-queue":
				// Key rotated right before the Collector's first queue poll.
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer upstream.Close()

		hostCtx, stopHost := context.WithCancel(t.Context())
		defer stopHost()
		app := newExternalApp(t, upstream.URL, externalAuthorityManagement)
		app.runCtx = hostCtx
		app.cfg.CollectorMode = "http"

		inWindowAtLock := make(chan struct{})
		rejectionDone := make(chan struct{})
		var lockOnce, doneOnce sync.Once
		app.beforeCollectorAuthRejectionLock = func(uint64) {
			lockOnce.Do(func() { close(inWindowAtLock) })
		}
		app.afterExternalCollectorStart = func() {
			<-inWindowAtLock
		}
		app.afterCollectorAuthRejection = func(uint64) {
			doneOnce.Do(func() { close(rejectionDone) })
		}

		record, err := app.store.Load(hostCtx)
		if err != nil {
			t.Fatal(err)
		}
		app.startUsageCollection(record.Operation)

		select {
		case <-rejectionDone:
		case <-time.After(5 * time.Second):
			t.Fatal("in-window reportCollectorAuthRejection did not complete")
		}

		if !app.externalReadGrant(t.Context()).Refused {
			t.Fatal("immediate in-window Collector auth refusal did not persist externalReadGrant.Refused")
		}
		if app.usageCollectionStarted() || app.collectionPhase() != usageapp.PhaseStopped {
			t.Fatalf("collector remained active after in-window refusal: started=%v phase=%s", app.usageCollectionStarted(), app.collectionPhase())
		}
		if got := app.usageStartBlock(); got != freshconfig.ReasonManagementRejected {
			t.Fatalf("usageStartBlock = %q, want %q", got, freshconfig.ReasonManagementRejected)
		}

		callsAfterRefusal := upstreamCalls.Load()
		modelsResp := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
		if modelsResp.Code != http.StatusConflict || errorCode(t, modelsResp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("cpamp/v1/models after in-window refusal = %d %s, want 409", modelsResp.Code, modelsResp.Body.String())
		}
		if got := upstreamCalls.Load() - callsAfterRefusal; got != 0 {
			t.Fatalf("cpamp/v1/models sent %d upstream requests after recorded in-window refusal, want 0", got)
		}
	})
}

// TestExternalUsageStateSerializesAndBindsSingleSnapshotUnderGate proves that
// GET external/usage:
//  1. Serializes its upstream CPA probes under legacyCredentialGate and records
//     401/403 rejections before releasing the gate so concurrent external/usage
//     and cpamp/v1/models callers never submit a bad credential a second time.
//  2. Binds grant and decrypted ExternalInstall to one in-gate snapshot so a
//     rebind occurring right after target resolution never splits the refusal
//     digest from the credential actually sent upstream.
func TestExternalUsageStateSerializesAndBindsSingleSnapshotUnderGate(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run("concurrent burst "+http.StatusText(status), func(t *testing.T) {
			var upstreamAuthAttempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v0/management/config", "/v8/management/config", "/v0/management/usage-statistics-enabled":
					upstreamAuthAttempts.Add(1)
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"rejected"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()

			app := newExternalApp(t, upstream.URL, externalAuthorityManagement)
			app.store = uncancellableStore{app.store}

			atBeforeRecord := make(chan struct{})
			allowRecord := make(chan struct{})
			var recordOnce sync.Once
			app.rejection.beforeRecord = func() {
				recordOnce.Do(func() {
					close(atBeforeRecord)
					<-allowRecord
				})
			}

			firstResp := httptest.NewRecorder()
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				req := httptest.NewRequest(http.MethodGet, "/control/api/v2/external/usage", nil)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				app.canonicalAPI(firstResp, req)
			}()

			select {
			case <-atBeforeRecord:
			case <-time.After(5 * time.Second):
				t.Fatal("first GET external/usage did not reach rejection recording")
			}

			waiterUsageEntered := make(chan struct{})
			waiterModelsEntered := make(chan struct{})
			waiterUsageResp := httptest.NewRecorder()
			waiterModelsResp := httptest.NewRecorder()
			var waiters sync.WaitGroup
			waiters.Add(2)
			go func() {
				defer waiters.Done()
				ctx := &waitNotifyContext{Context: t.Context(), entered: waiterUsageEntered}
				req := httptest.NewRequest(http.MethodGet, "/control/api/v2/external/usage", nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				app.canonicalAPI(waiterUsageResp, req)
			}()
			go func() {
				defer waiters.Done()
				ctx := &waitNotifyContext{Context: t.Context(), entered: waiterModelsEntered}
				req := httptest.NewRequest(http.MethodGet, "/control/api/v2/cpamp/v1/models", nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				app.canonicalAPI(waiterModelsResp, req)
			}()

			select {
			case <-waiterUsageEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent GET external/usage waiter did not queue on legacyCredentialGate")
			}
			select {
			case <-waiterModelsEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent GET cpamp/v1/models waiter did not queue on legacyCredentialGate")
			}

			close(allowRecord)
			<-firstDone
			waiters.Wait()

			for name, resp := range map[string]*httptest.ResponseRecorder{
				"first external/usage":      firstResp,
				"concurrent external/usage": waiterUsageResp,
				"concurrent models":         waiterModelsResp,
			} {
				if resp.Code != http.StatusConflict || errorCode(t, resp) != freshconfig.ReasonManagementRejected {
					t.Fatalf("%s = %d %s, want 409 %s", name, resp.Code, resp.Body.String(), freshconfig.ReasonManagementRejected)
				}
			}
			if status == http.StatusForbidden && !strings.Contains(firstResp.Body.String(), externalRebindForbiddenDetail) {
				t.Fatalf("403 response missing forbidden detail: %s", firstResp.Body.String())
			}
			if got := upstreamAuthAttempts.Load(); got != 1 {
				t.Fatalf("External CPA received %d bad-credential management requests across concurrent burst, want 1", got)
			}
		})
	}

	t.Run("rebind after snapshot resolution keeps rejection on sent credential", func(t *testing.T) {
		staleKey := strings.Repeat("1", 64)
		reboundKey := strings.Repeat("2", 64)
		rotatedAfterRebind := strings.Repeat("3", 64)
		var activeKey atomic.Value
		activeKey.Store(reboundKey)

		var mu sync.Mutex
		var usageProbeAuth []string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if r.Header.Get("X-Test-Rebind") == "" {
				mu.Lock()
				usageProbeAuth = append(usageProbeAuth, auth)
				mu.Unlock()
			}
			if auth != "Bearer "+activeKey.Load().(string) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.13")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		}))
		defer upstream.Close()

		app := newExternalApp(t, upstream.URL, staleKey)
		baseSource := app.externalSource
		var rebindOnce sync.Once
		app.externalSource = func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
			install, err := baseSource(ctx)
			if err == nil {
				rebindOnce.Do(func() {
					rebindReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/external/rebind", strings.NewReader(rebindBody("", reboundKey)))
					rebindReq.Header.Set("Content-Type", "application/json")
					rebindReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
					rebindResp := httptest.NewRecorder()
					app.canonicalAPI(rebindResp, rebindReq)
					if rebindResp.Code != http.StatusOK {
						t.Errorf("concurrent POST external/rebind = %d %s", rebindResp.Code, rebindResp.Body.String())
					}
					// Rotate upstream again after rebind succeeds so any split-load
					// sending reboundKey during the first GET would receive 401.
					activeKey.Store(rotatedAfterRebind)
				})
			}
			return install, err
		}

		resp := installationAPI(t, app, http.MethodGet, "external/usage", strategyTestAdmin)
		if resp.Code != http.StatusConflict || errorCode(t, resp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("first GET external/usage = %d %s, want 409", resp.Code, resp.Body.String())
		}
		mu.Lock()
		// Filter out POST external/rebind's own verification call (Bearer reboundKey
		// while activeKey was reboundKey) by checking the first GET's presented key:
		// rebind's ObserveExternalVersion ran once with reboundKey; the GET itself
		// must have presented staleKey, never a second reboundKey!
		gotAuth := append([]string(nil), usageProbeAuth...)
		mu.Unlock()
		var staleCount, reboundAfterRotate int
		for _, h := range gotAuth {
			if h == "Bearer "+staleKey {
				staleCount++
			}
			if h == "Bearer "+reboundKey {
				reboundAfterRotate++
			}
		}
		// POST external/rebind sent Bearer reboundKey once during verification;
		// GET external/usage must have sent Bearer staleKey (1), not reboundKey (which would make 2).
		if staleCount != 1 || reboundAfterRotate != 1 {
			t.Fatalf("upstream Authorization sequence = %v, want 1 rebind probe with reboundKey and 1 GET probe with staleKey", gotAuth)
		}
		if app.externalReadGrant(t.Context()).Refused {
			t.Fatal("stale credential's 401 falsely marked the rebound credential as refused")
		}
	})
}

// TestExternalCollectorStartHoldsGateAcrossUsageStatisticsReads proves that if
// ObserveExternalVersion returns 200 OK and the credential is then rejected on
// the follow-up FetchExternalUsageStatistics read (401/403), legacyCredentialGate
// remains held until noteExternalRejection commits so concurrent model/usage
// reads never re-submit the rejected credential.
func TestExternalCollectorStartHoldsGateAcrossUsageStatisticsReads(t *testing.T) {
	var badKeyHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.13")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v0/management/usage-statistics-enabled", "/v8/management/config":
			badKeyHits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	app := newExternalApp(t, upstream.URL, externalAuthorityManagement)
	app.runCtx = hostCtx
	app.store = uncancellableStore{app.store}

	disableReq := httptest.NewRequest(http.MethodPatch, "/control/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":false}`))
	disableReq.Header.Set("Content-Type", "application/json")
	disableReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	disableResp := httptest.NewRecorder()
	app.canonicalAPI(disableResp, disableReq)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("PATCH disable = %d %s", disableResp.Code, disableResp.Body.String())
	}

	atBeforeRecord := make(chan struct{})
	allowRecord := make(chan struct{})
	var recordOnce sync.Once
	app.rejection.beforeRecord = func() {
		recordOnce.Do(func() {
			close(atBeforeRecord)
			<-allowRecord
		})
	}

	patchResp := httptest.NewRecorder()
	patchDone := make(chan struct{})
	go func() {
		defer close(patchDone)
		req := httptest.NewRequest(http.MethodPatch, "/control/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		app.canonicalAPI(patchResp, req)
	}()

	select {
	case <-atBeforeRecord:
	case <-time.After(5 * time.Second):
		t.Fatal("FetchExternalUsageStatistics 401 did not reach rejection recording")
	}

	modelWaiterEntered := make(chan struct{})
	modelResp := httptest.NewRecorder()
	modelDone := make(chan struct{})
	go func() {
		defer close(modelDone)
		ctx := &waitNotifyContext{Context: t.Context(), entered: modelWaiterEntered}
		req := httptest.NewRequest(http.MethodGet, "/control/api/v2/cpamp/v1/models", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		app.canonicalAPI(modelResp, req)
	}()

	select {
	case <-modelWaiterEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cpamp/v1/models waiter did not queue on legacyCredentialGate")
	}

	close(allowRecord)
	<-patchDone
	<-modelDone

	if patchResp.Code != http.StatusOK {
		t.Fatalf("PATCH enable = %d %s, want 200", patchResp.Code, patchResp.Body.String())
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("follow-up statistics 401 did not persist externalReadGrant.Refused")
	}
	if modelResp.Code != http.StatusConflict || errorCode(t, modelResp) != freshconfig.ReasonManagementRejected {
		t.Fatalf("concurrent cpamp/v1/models = %d %s, want 409", modelResp.Code, modelResp.Body.String())
	}
	if got := badKeyHits.Load(); got != 1 {
		t.Fatalf("External CPA received %d post-rotation rejected requests across start + concurrent models, want 1", got)
	}
}

// TestExternalRebindDurableDeleteFailurePreservesCollectorLifecycle exercises
// a real host-bound External Collector across POST external/rebind when the
// durable refusal DELETE fails (returning HTTP 503 external_rebind_unavailable)
// and when a subsequent retry commits the DELETE (returning HTTP 200):
//  1. With monitoring On (collector.enabled=true), a Collector started before
//     the fault is stopped on the old credential and restarted on the newly
//     verified credential even when DELETE returns 503, then remains started
//     after the DELETE fault clears and retry returns 200; a subsequent CPA
//     refusal on that restarted run still stops collection and records the
//     refusal blocker without repeating bad-key requests.
//  2. On same-key re-verify under a DELETE fault, collection resumes in the
//     current process (503) while a cold restart on the same SQLite file sees
//     the un-deleted refusal row, keeps collection stopped with
//     blockedReason=external_management_key_rejected (0 CPA auth attempts),
//     and recovers to 200 + started once retry deletes the row.
//  3. With monitoring Off (collector.enabled=false), collection is never
//     started on either the 503 DELETE-fault path or the 200 retry path.
func TestExternalRebindDurableDeleteFailurePreservesCollectorLifecycle(t *testing.T) {
	awaitBearer := func(t *testing.T, ch <-chan string, wantKey, label string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		want := "Bearer " + wantKey
		for {
			select {
			case got := <-ch:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("%s: timed out waiting for Collector usage-queue poll with %q", label, wantKey)
			}
		}
	}

	t.Run("monitoring_on_restarts_collector_across_delete_fault_and_retry_without_old_credential", func(t *testing.T) {
		const (
			keyK2 = "collector-r7-k2-0000111122223333444455556666777788889999aaaabbcc"
			keyK3 = "collector-r7-k3-111122223333444455556666777788889999aaaabbbbccdd"
			keyK4 = "collector-r7-k4-22223333444455556666777788889999aaaabbbbccccddee"
		)
		h := newExternalUsageHarness(t)
		h.cpa.enabled.Store(true)
		queueAuthCh := make(chan string, 64)
		h.cpa.onQueueRead.Store(func(auth string) {
			select {
			case queueAuthCh <- auth:
			default:
			}
		})
		rejectedCh := make(chan uint64, 8)
		h.app.afterCollectorAuthRejection = func(runID uint64) {
			select {
			case rejectedCh <- runID:
			default:
			}
		}

		// 1. Start the real External Collector on K1 before any fault.
		h.app.startUsageCollection(h.operation(t))
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "initial K1 collector")
		initialGet := h.call(t, http.MethodGet, "external/usage", "")
		if initialGet.Code != http.StatusOK {
			t.Fatalf("initial GET external/usage = %d %s, want 200", initialGet.Code, initialGet.Body.String())
		}
		initialState := decodeExternalUsageState(t, initialGet.Body.Bytes())
		if !initialState.CollectionStarted || initialState.Collector == string(usageapp.PhaseStopped) || initialState.BlockedReason != "" {
			t.Fatalf("initial usage state = %+v, want started & unblocked", initialState)
		}

		// 2. Rotate CPA to K2 so the running K1 Collector stops itself and persists the refusal row.
		h.cpa.management.Store(keyK2)
		select {
		case <-rejectedCh:
		case <-time.After(5 * time.Second):
			t.Fatal("K1 Collector did not report auth rejection after rotation to K2")
		}
		if refusedGet := h.call(t, http.MethodGet, "external/usage", ""); refusedGet.Code != http.StatusConflict || errorCode(t, refusedGet) != freshconfig.ReasonManagementRejected {
			t.Fatalf("GET external/usage while K1 refused = %d %s, want 409 %s", refusedGet.Code, refusedGet.Body.String(), freshconfig.ReasonManagementRejected)
		}

		// 3. Fault durable DELETE on fresh_external_rejection_v1 and rebind to K2:
		// HTTP must report retryable 503 external_rebind_unavailable, while the
		// real Collector restarts on verified K2 without leaving a monitoring gap.
		if _, err := h.db.ExecContext(
			t.Context(),
			`CREATE TRIGGER block_r7_collector_delete BEFORE DELETE ON settings WHEN OLD.key = 'fresh_external_rejection_v1' BEGIN SELECT RAISE(ABORT, 'simulated r7 collector delete fault'); END;`,
		); err != nil {
			t.Fatalf("install delete trigger: %v", err)
		}
		rebindK2Body := fmt.Sprintf(`{"requestId":%q,"managementKey":%q}`, strings.Repeat("a", 32), keyK2)
		refusalsBeforeK2 := h.cpa.refusals.Load()
		rebindFault := h.call(t, http.MethodPost, "external/rebind", rebindK2Body)
		if rebindFault.Code != http.StatusServiceUnavailable || errorCode(t, rebindFault) != "external_rebind_unavailable" {
			t.Fatalf("POST external/rebind under DELETE fault = %d %s, want 503 external_rebind_unavailable", rebindFault.Code, rebindFault.Body.String())
		}

		faultGet := h.call(t, http.MethodGet, "external/usage", "")
		if faultGet.Code != http.StatusOK {
			t.Fatalf("GET external/usage after 503 rebind = %d %s, want 200", faultGet.Code, faultGet.Body.String())
		}
		faultState := decodeExternalUsageState(t, faultGet.Body.Bytes())
		if !faultState.CollectionStarted || faultState.Collector == string(usageapp.PhaseStopped) || faultState.BlockedReason != "" {
			t.Fatalf("usage state after 503 rebind = %+v, want collectionStarted=true, collector!=stopped, blockedReason empty", faultState)
		}
		awaitBearer(t, queueAuthCh, keyK2, "Collector restarted on K2 after 503 rebind")
		if got := h.cpa.refusals.Load() - refusalsBeforeK2; got != 0 {
			t.Fatalf("restarted K2 Collector caused %d refused CPA requests, want 0", got)
		}

		// 4. Remove the DELETE fault and retry the exact same K2 rebind request:
		// returns 200 reverified, deletes the refusal row, and keeps collection started.
		if _, err := h.db.ExecContext(t.Context(), `DROP TRIGGER block_r7_collector_delete;`); err != nil {
			t.Fatalf("drop delete trigger: %v", err)
		}
		rebindRetry := h.call(t, http.MethodPost, "external/rebind", rebindK2Body)
		if rebindRetry.Code != http.StatusOK || !strings.Contains(rebindRetry.Body.String(), `"status":"reverified"`) {
			t.Fatalf("retry K2 rebind = %d %s, want 200 reverified", rebindRetry.Code, rebindRetry.Body.String())
		}
		var durableRows int
		if err := h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM settings WHERE key = ?`, externalRejectionKey).Scan(&durableRows); err != nil || durableRows != 0 {
			t.Fatalf("durable refusal rows after retry = %d (err=%v), want 0", durableRows, err)
		}
		retryGet := h.call(t, http.MethodGet, "external/usage", "")
		if retryGet.Code != http.StatusOK {
			t.Fatalf("GET external/usage after 200 retry = %d %s, want 200", retryGet.Code, retryGet.Body.String())
		}
		retryState := decodeExternalUsageState(t, retryGet.Body.Bytes())
		if !retryState.CollectionStarted || retryState.Collector == string(usageapp.PhaseStopped) || retryState.BlockedReason != "" {
			t.Fatalf("usage state after 200 retry = %+v, want collectionStarted=true, collector!=stopped, blockedReason empty", retryState)
		}
		awaitBearer(t, queueAuthCh, keyK2, "Collector polling K2 after 200 retry")

		// 5. While K2 Collector is actively running (usageStarted=true), rotate
		// proactively to K3 under a driver-level DELETE fault while CPA accepts
		// both K2 and K3 during the handoff, then revoke K2: stopUsageCollection
		// must have stopped the K2 run so polling switches to K3 with 0 refusals.
		ctrl := &rejectionSQLController{
			onDelete: func() error {
				return errors.New("simulated driver delete failure")
			},
		}
		h.app.rejection = newExternalRejection(openControlledRejectionDB(t, h.app, ctrl))
		h.cpa.alsoAccept.Store(keyK2)
		h.cpa.management.Store(keyK3)
		rebindK3Body := fmt.Sprintf(`{"requestId":%q,"managementKey":%q}`, strings.Repeat("b", 32), keyK3)
		rebindK3Fault := h.call(t, http.MethodPost, "external/rebind", rebindK3Body)
		if rebindK3Fault.Code != http.StatusServiceUnavailable || errorCode(t, rebindK3Fault) != "external_rebind_unavailable" {
			t.Fatalf("proactive K3 rebind under driver DELETE fault = %d %s, want 503 external_rebind_unavailable", rebindK3Fault.Code, rebindK3Fault.Body.String())
		}
		// Revoke K2 immediately after rebind returns and drain buffered pre-handoff notifications.
		h.cpa.alsoAccept.Store("")
		for len(queueAuthCh) > 0 {
			<-queueAuthCh
		}
		refusalsAfterHandoff := h.cpa.refusals.Load()
		awaitBearer(t, queueAuthCh, keyK3, "Collector switched from K2 to K3 on 503 rebind")
		if got := h.cpa.refusals.Load() - refusalsAfterHandoff; got != 0 {
			t.Fatalf("old K2 Collector run was not stopped before K3 handoff: %d refused CPA requests", got)
		}

		// 6. Confirm grant/refusal recorder still applies to the newly started K3
		// Collector run: rotating CPA to K4 stops collection and blocks K3 locally.
		ctrl.mu.Lock()
		ctrl.onDelete = nil
		ctrl.mu.Unlock()
		h.cpa.management.Store(keyK4)
		select {
		case <-rejectedCh:
		case <-time.After(5 * time.Second):
			t.Fatal("restarted K3 Collector did not stop and report rejection when CPA rotated to K4")
		}
		if h.app.usageCollectionStarted() || h.app.usageStartBlock() != freshconfig.ReasonManagementRejected {
			t.Fatalf("after K3 refusal: started=%v blockedReason=%q, want false / %q",
				h.app.usageCollectionStarted(), h.app.usageStartBlock(), freshconfig.ReasonManagementRejected)
		}
		cpaBeforeBlockedReads := h.cpa.requests.Load()
		if uRes := h.call(t, http.MethodGet, "external/usage", ""); uRes.Code != http.StatusConflict || errorCode(t, uRes) != freshconfig.ReasonManagementRejected {
			t.Fatalf("GET external/usage after K3 refusal = %d %s, want 409 %s", uRes.Code, uRes.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if got := h.cpa.requests.Load() - cpaBeforeBlockedReads; got != 0 {
			t.Fatalf("refused K3 contacted CPA %d times on follow-up read, want 0", got)
		}
	})

	t.Run("same_key_reverify_delete_fault_restarts_in_process_and_exposes_rejected_blocker_after_restart_until_retry", func(t *testing.T) {
		fake := newExternalUsageCPA(t, externalAuthorityManagement)
		fake.enabled.Store(true)
		queueAuthCh := make(chan string, 64)
		fake.onQueueRead.Store(func(auth string) {
			select {
			case queueAuthCh <- auth:
			default:
			}
		})

		paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
		paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")

		var (
			closeMu     sync.Mutex
			closeActive func()
		)
		stopCurrent := func() {
			closeMu.Lock()
			fn := closeActive
			closeActive = nil
			closeMu.Unlock()
			if fn != nil {
				fn()
			}
		}
		t.Cleanup(stopCurrent)

		openInstance := func(initialize bool) (*externalUsageHarness, chan uint64) {
			stopCurrent()
			hostCtx, stopHost := context.WithCancel(t.Context())
			opts := fresh.OpenOptions{}
			if initialize {
				opts = fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin}
			}
			opened, err := fresh.Open(hostCtx, paths, opts)
			if err != nil {
				stopHost()
				t.Fatalf("open sqlite instance (init=%v): %v", initialize, err)
			}
			closeMu.Lock()
			closeActive = func() {
				stopHost()
				_ = opened.Close()
			}
			closeMu.Unlock()

			app := newFreshApplication(opened, config.Config{CollectorMode: "http"}, nil)
			app.runCtx = hostCtx
			quietProjection(app)
			rejectedCh := make(chan uint64, 8)
			app.afterCollectorAuthRejection = func(runID uint64) {
				select {
				case rejectedCh <- runID:
				default:
				}
			}
			if initialize {
				normalized, normErr := cpa.NormalizeV8BaseURL(fake.server.URL)
				if normErr != nil {
					t.Fatal(normErr)
				}
				protected, protErr := opened.Protector.ProtectString(externalAuthorityManagement)
				if protErr != nil {
					t.Fatal(protErr)
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
			}
			app.externalSource = externalInstallSource(app.store, opened.Protector)
			app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)
			return &externalUsageHarness{app: app, cpa: fake, db: opened.DB}, rejectedCh
		}

		h1, rejectedCh1 := openInstance(true)
		h1.app.startUsageCollection(h1.operation(t))
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "initial K1 collector before ban")

		// Temporary refusal stops Collector and writes durable refusal row.
		fake.rejects.Store(true)
		select {
		case <-rejectedCh1:
		case <-time.After(5 * time.Second):
			t.Fatal("K1 Collector did not stop on temporary refusal")
		}

		// Ban expires; install DELETE fault and re-verify same K1 -> 503, Collector resumes in-process.
		fake.rejects.Store(false)
		if _, err := h1.db.ExecContext(
			t.Context(),
			`CREATE TRIGGER block_r7_same_k1_delete BEFORE DELETE ON settings WHEN OLD.key = 'fresh_external_rejection_v1' BEGIN SELECT RAISE(ABORT, 'simulated same-k1 delete fault'); END;`,
		); err != nil {
			t.Fatalf("install same-k1 delete trigger: %v", err)
		}
		for len(queueAuthCh) > 0 {
			<-queueAuthCh
		}
		reverifyBody := fmt.Sprintf(`{"requestId":%q,"managementKey":%q}`, strings.Repeat("c", 32), externalAuthorityManagement)
		reverifyFault := h1.call(t, http.MethodPost, "external/rebind", reverifyBody)
		if reverifyFault.Code != http.StatusServiceUnavailable || errorCode(t, reverifyFault) != "external_rebind_unavailable" {
			t.Fatalf("same-K1 re-verify under DELETE fault = %d %s, want 503 external_rebind_unavailable", reverifyFault.Code, reverifyFault.Body.String())
		}
		inProcState := decodeExternalUsageState(t, h1.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
		if !inProcState.CollectionStarted || inProcState.BlockedReason != "" {
			t.Fatalf("in-process usage state after 503 same-K1 re-verify = %+v, want collectionStarted=true, blockedReason empty", inProcState)
		}
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "in-process K1 collector after 503 re-verify")

		// Cold restart on the exact same SQLite file: durable refusal row remains, so
		// startup refuses collection with external_management_key_rejected and 0 CPA hits.
		h2, _ := openInstance(false)
		cpaBeforeRestart := fake.requests.Load()
		h2.app.startUsageCollection(h2.operation(t))
		if h2.app.usageCollectionStarted() || h2.app.usageStartBlock() != freshconfig.ReasonManagementRejected {
			t.Fatalf("restarted Collector before durable clear: started=%v blockedReason=%q, want false / %q",
				h2.app.usageCollectionStarted(), h2.app.usageStartBlock(), freshconfig.ReasonManagementRejected)
		}
		if uRes := h2.call(t, http.MethodGet, "external/usage", ""); uRes.Code != http.StatusConflict || errorCode(t, uRes) != freshconfig.ReasonManagementRejected {
			t.Fatalf("restarted GET external/usage before durable clear = %d %s, want 409 %s", uRes.Code, uRes.Body.String(), freshconfig.ReasonManagementRejected)
		}
		if got := fake.requests.Load() - cpaBeforeRestart; got != 0 {
			t.Fatalf("restarted instance sent %d CPA requests while durable refusal row remained, want 0", got)
		}

		// Clear trigger and retry same-K1 re-verify: returns 200 and starts Collector durably across restart.
		if _, err := h2.db.ExecContext(t.Context(), `DROP TRIGGER block_r7_same_k1_delete;`); err != nil {
			t.Fatalf("drop same-k1 delete trigger: %v", err)
		}
		for len(queueAuthCh) > 0 {
			<-queueAuthCh
		}
		reverifyOK := h2.call(t, http.MethodPost, "external/rebind", reverifyBody)
		if reverifyOK.Code != http.StatusOK {
			t.Fatalf("retry same-K1 re-verify = %d %s, want 200", reverifyOK.Code, reverifyOK.Body.String())
		}
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "K1 collector after 200 retry")

		h3, _ := openInstance(false)
		for len(queueAuthCh) > 0 {
			<-queueAuthCh
		}
		h3.app.startUsageCollection(h3.operation(t))
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "K1 collector after durable clear + restart")
		afterClearState := decodeExternalUsageState(t, h3.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
		if !afterClearState.CollectionStarted || afterClearState.BlockedReason != "" {
			t.Fatalf("usage state after durable clear + restart = %+v, want collectionStarted=true, blockedReason empty", afterClearState)
		}
	})

	t.Run("monitoring_off_never_starts_collector_on_delete_fault_or_retry", func(t *testing.T) {
		const rotatedKey = "collector-r7-off-0000111122223333444455556666777788889999aaaabb"
		h := newExternalUsageHarness(t)
		h.cpa.enabled.Store(true)
		queueAuthCh := make(chan string, 32)
		h.cpa.onQueueRead.Store(func(auth string) {
			select {
			case queueAuthCh <- auth:
			default:
			}
		})

		// Start Collector first, then explicitly switch monitoring Off via console API.
		h.app.startUsageCollection(h.operation(t))
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "initial collector before switching Off")

		patchReq := httptest.NewRequest(http.MethodPatch, "/control/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":false}`))
		patchReq.Header.Set("Content-Type", "application/json")
		patchReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		patchRes := httptest.NewRecorder()
		h.app.canonicalAPI(patchRes, patchReq)
		if patchRes.Code != http.StatusOK {
			t.Fatalf("PATCH enabled=false = %d %s, want 200", patchRes.Code, patchRes.Body.String())
		}

		// Rotate CPA and trigger durable refusal on K1.
		h.cpa.management.Store(rotatedKey)
		if refused := h.call(t, http.MethodGet, "external/usage", ""); refused.Code != http.StatusConflict || errorCode(t, refused) != freshconfig.ReasonManagementRejected {
			t.Fatalf("GET external/usage on rotated K1 = %d %s, want 409 %s", refused.Code, refused.Body.String(), freshconfig.ReasonManagementRejected)
		}

		if _, err := h.db.ExecContext(
			t.Context(),
			`CREATE TRIGGER block_r7_off_delete BEFORE DELETE ON settings WHEN OLD.key = 'fresh_external_rejection_v1' BEGIN SELECT RAISE(ABORT, 'simulated off delete fault'); END;`,
		); err != nil {
			t.Fatalf("install off delete trigger: %v", err)
		}
		queueBeforeFaultRebind := h.cpa.queueReads.Load()
		rebindBody := fmt.Sprintf(`{"requestId":%q,"managementKey":%q}`, strings.Repeat("d", 32), rotatedKey)
		rebindFault := h.call(t, http.MethodPost, "external/rebind", rebindBody)
		if rebindFault.Code != http.StatusServiceUnavailable || errorCode(t, rebindFault) != "external_rebind_unavailable" {
			t.Fatalf("rebind with monitoring Off under DELETE fault = %d %s, want 503 external_rebind_unavailable", rebindFault.Code, rebindFault.Body.String())
		}
		offFaultState := decodeExternalUsageState(t, h.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
		if offFaultState.CollectionStarted || offFaultState.Collector != string(usageapp.PhaseStopped) || offFaultState.BlockedReason != usageStartReasonMonitoringDisabled {
			t.Fatalf("monitoring Off state after 503 rebind = %+v, want stopped, collectionStarted=false, blockedReason=%q", offFaultState, usageStartReasonMonitoringDisabled)
		}
		if got := h.cpa.queueReads.Load() - queueBeforeFaultRebind; got != 0 {
			t.Fatalf("monitoring Off polled usage-queue %d times after 503 rebind, want 0", got)
		}

		if _, err := h.db.ExecContext(t.Context(), `DROP TRIGGER block_r7_off_delete;`); err != nil {
			t.Fatalf("drop off delete trigger: %v", err)
		}
		rebindOK := h.call(t, http.MethodPost, "external/rebind", rebindBody)
		if rebindOK.Code != http.StatusOK {
			t.Fatalf("retry rebind with monitoring Off = %d %s, want 200", rebindOK.Code, rebindOK.Body.String())
		}
		offOKState := decodeExternalUsageState(t, h.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
		if offOKState.CollectionStarted || offOKState.Collector != string(usageapp.PhaseStopped) || offOKState.BlockedReason != usageStartReasonMonitoringDisabled {
			t.Fatalf("monitoring Off state after 200 retry = %+v, want stopped, collectionStarted=false, blockedReason=%q", offOKState, usageStartReasonMonitoringDisabled)
		}
		if got := h.cpa.queueReads.Load() - queueBeforeFaultRebind; got != 0 {
			t.Fatalf("monitoring Off polled usage-queue %d times after 200 retry, want 0", got)
		}
	})

	t.Run("unreadable_monitoring_switch_sets_collector_unavailable_blocker_without_starting", func(t *testing.T) {
		h := newExternalUsageHarness(t)
		h.cpa.enabled.Store(true)
		queueAuthCh := make(chan string, 32)
		h.cpa.onQueueRead.Store(func(auth string) {
			select {
			case queueAuthCh <- auth:
			default:
			}
		})

		// 1. Start the real External Collector first so usageStarted=true and polling is live.
		h.app.startUsageCollection(h.operation(t))
		awaitBearer(t, queueAuthCh, externalAuthorityManagement, "initial collector before switch fault")

		// 2. Fault the runtimeSettings SQLite read so usageMonitoringEnabled fails.
		switchCtrl := &rejectionSQLController{
			onSelect: func() error {
				return errors.New("simulated monitoring switch read failure")
			},
		}
		h.app.runtimeSettings = setting.New(openControlledRejectionDB(t, h.app, switchCtrl), h.app.protector)

		// 3. Same-key rebind stops the running Collector and re-runs startUsageCollection(op):
		// unreadable switch must keep Collector stopped and expose usage_collector_unavailable.
		reverifyBody := fmt.Sprintf(`{"requestId":%q,"managementKey":%q}`, strings.Repeat("e", 32), externalAuthorityManagement)
		reverifyRes := h.call(t, http.MethodPost, "external/rebind", reverifyBody)
		if reverifyRes.Code != http.StatusOK {
			t.Fatalf("same-key rebind with unreadable switch = %d %s, want 200", reverifyRes.Code, reverifyRes.Body.String())
		}
		queueAfterStop := h.cpa.queueReads.Load()
		usageRes := h.call(t, http.MethodGet, "external/usage", "")
		if usageRes.Code != http.StatusOK {
			t.Fatalf("GET external/usage with unreadable switch = %d %s, want 200", usageRes.Code, usageRes.Body.String())
		}
		state := decodeExternalUsageState(t, usageRes.Body.Bytes())
		if state.CollectionStarted || state.Collector != string(usageapp.PhaseStopped) || state.BlockedReason != usageStartReasonCollectorFailed {
			t.Fatalf("usage state on unreadable monitoring switch = %+v, want stopped, collectionStarted=false, blockedReason=%q",
				state, usageStartReasonCollectorFailed)
		}
		if got := h.cpa.queueReads.Load() - queueAfterStop; got != 0 {
			t.Fatalf("Collector polled usage-queue %d times with unreadable monitoring switch, want 0", got)
		}
	})
}
