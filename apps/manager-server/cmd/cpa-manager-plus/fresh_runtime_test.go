package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type freshRuntimeTestClient struct {
	runtimeservice.RuntimeClient
	mu            sync.Mutex
	status        model.RuntimeObservedStatus
	statusErr     error
	onStatus      func(context.Context)
	startResult   model.RuntimeOperationResult
	startErr      error
	stopResult    model.RuntimeOperationResult
	stopErr       error
	onStop        func()
	onStopRequest func(context.Context, model.RuntimeMutationRequest)
	restartResult model.RuntimeOperationResult
	restartErr    error
	starts        int
	stops         int
	restarts      int
	startRequests []model.RuntimeMutationRequest
}

func (c *freshRuntimeTestClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	c.mu.Lock()
	onStatus := c.onStatus
	c.mu.Unlock()
	if onStatus != nil {
		onStatus(ctx)
	}
	if err := ctx.Err(); err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, c.statusErr
}

func (c *freshRuntimeTestClient) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	c.startRequests = append(c.startRequests, request)
	if c.startResult.OperationID == "" {
		c.startResult = freshRuntimeResult(request, model.RuntimeOperationStart, model.RuntimeOperationSucceeded)
	}
	return c.startResult, c.startErr
}

func (c *freshRuntimeTestClient) Stop(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.mu.Lock()
	c.stops++
	if c.stopResult.OperationID == "" {
		c.stopResult = freshRuntimeResult(request, model.RuntimeOperationStop, model.RuntimeOperationSucceeded)
	}
	res, err := c.stopResult, c.stopErr
	onStop := c.onStop
	onStopRequest := c.onStopRequest
	c.mu.Unlock()
	if onStopRequest != nil {
		onStopRequest(ctx, request)
	}
	if onStop != nil {
		onStop()
	}
	return res, err
}

func (c *freshRuntimeTestClient) Restart(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.restarts++
	if c.restartResult.OperationID == "" {
		c.restartResult = freshRuntimeResult(request, model.RuntimeOperationRestart, model.RuntimeOperationSucceeded)
	}
	return c.restartResult, c.restartErr
}

func freshRuntimeResult(request model.RuntimeMutationRequest, operation model.RuntimeOperationType, state model.RuntimeOperationState) model.RuntimeOperationResult {
	return model.RuntimeOperationResult{
		OperationID: request.OperationID, OperationType: operation,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State: state,
	}
}

func freshRuntimeReadyStatus(capabilities ...model.RuntimeCapability) model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity: "runtime-test", Generation: 7, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities: capabilities, Recovery: &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive},
	}
}

func freshRuntimeRequest(t *testing.T, action, requestID string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]string{"action": action, "requestId": requestID})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/runtime/operations", bytes.NewReader(body))
}

func freshRuntimeProductCompleteOperation(t *testing.T, app *freshApplication) bootstrap.FreshOperation {
	t.Helper()
	op, err := app.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	op.ProductComplete = true
	return op
}

func TestFreshRuntimeOperationsCapabilityIdempotencyAndConflict(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart)}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)

	unsupported := httptest.NewRecorder()
	app.runtimeOperations(unsupported, freshRuntimeRequest(t, "stop", "stop-1"), op, "runtime/operations")
	if unsupported.Code != http.StatusConflict || unsupported.Body.String() == "" {
		t.Fatalf("capability refusal = %d %s", unsupported.Code, unsupported.Body.String())
	}

	first := httptest.NewRecorder()
	app.runtimeOperations(first, freshRuntimeRequest(t, "start", "start-1"), op, "runtime/operations")
	if first.Code != http.StatusOK || client.starts != 1 {
		t.Fatalf("first start = %d starts=%d body=%s", first.Code, client.starts, first.Body.String())
	}
	second := httptest.NewRecorder()
	app.runtimeOperations(second, freshRuntimeRequest(t, "start", "start-1"), op, "runtime/operations")
	if second.Code != http.StatusOK || client.starts != 1 || second.Body.String() != first.Body.String() {
		t.Fatalf("idempotent start = %d starts=%d first=%s second=%s", second.Code, client.starts, first.Body.String(), second.Body.String())
	}

	client.status.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop}
	accepted := model.RuntimeOperationResult{
		OperationID: "runtime-ui/v1:stop-1", OperationType: model.RuntimeOperationStop,
		RuntimeIdentity: "runtime-test", RuntimeGeneration: 7, State: model.RuntimeOperationAccepted,
	}
	client.stopResult = accepted
	stop := httptest.NewRecorder()
	app.runtimeOperations(stop, freshRuntimeRequest(t, "stop", "stop-1"), op, "runtime/operations")
	if stop.Code != http.StatusOK || client.stops != 1 {
		t.Fatalf("stop = %d stops=%d body=%s", stop.Code, client.stops, stop.Body.String())
	}
	conflict := httptest.NewRecorder()
	app.runtimeOperations(conflict, freshRuntimeRequest(t, "start", "start-2"), op, "runtime/operations")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting start = %d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestFreshRuntimeUnknownOperationCanBeQueriedWithoutResend(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeTestClient{
		status:   freshRuntimeReadyStatus(model.RuntimeCapabilityStart),
		startErr: errors.New("transport interrupted after acceptance"),
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)

	submitted := httptest.NewRecorder()
	app.runtimeOperations(submitted, freshRuntimeRequest(t, "start", "unknown-1"), op, "runtime/operations")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("unknown submit = %d body=%s", submitted.Code, submitted.Body.String())
	}
	queried := httptest.NewRecorder()
	app.runtimeOperations(queried, httptest.NewRequest(http.MethodGet, "/runtime/operations/runtime-ui/v1:unknown-1", nil), op, "runtime/operations/runtime-ui/v1:unknown-1")
	if queried.Code != http.StatusOK || client.starts != 1 || !bytes.Contains(queried.Body.Bytes(), []byte(`"state":"unknown"`)) {
		t.Fatalf("unknown query = %d starts=%d body=%s", queried.Code, client.starts, queried.Body.String())
	}
}

func TestFreshRuntimeRequiresProductComplete(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	app.runtime = &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart)}
	op, err := app.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.runtimeOperations(response, freshRuntimeRequest(t, "start", "before-complete"), op, "runtime/operations")
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("ProductComplete gate = %d body=%s", response.Code, response.Body.String())
	}
}

// The journal keeps every non-terminal entry plus only the most recent terminal
// ones, so the single settings row cannot grow without limit.
func TestFreshRuntimeJournalBoundsTerminalEntries(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	journal := app.runtimeJournal
	base := time.Now().UTC()
	data := freshRuntimeJournalData{Operations: map[string]freshRuntimeOperation{}}
	data.Operations["pending-stop"] = freshRuntimeOperation{
		RequestID: "pending-stop", Action: string(model.RuntimeOperationStop),
		OperationID: "runtime-ui/v1:pending-stop", State: freshRuntimeStateUnknown,
		CreatedAt: base, UpdatedAt: base,
	}
	for index := range freshRuntimeJournalTerminalRetention + 10 {
		requestID := fmt.Sprintf("done-%02d", index)
		data.Operations[requestID] = freshRuntimeOperation{
			RequestID: requestID, Action: string(model.RuntimeOperationStart),
			OperationID: "runtime-ui/v1:" + requestID, State: freshRuntimeStateSucceeded,
			CreatedAt: base.Add(time.Duration(index) * time.Second), UpdatedAt: base.Add(time.Duration(index) * time.Second),
		}
	}
	if err := journal.save(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	loaded, err := journal.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Operations) != freshRuntimeJournalTerminalRetention+1 {
		t.Fatalf("bounded journal retained %d entries", len(loaded.Operations))
	}
	if _, found := loaded.Operations["pending-stop"]; !found {
		t.Fatal("bounded journal dropped a non-terminal entry")
	}
	if _, found := loaded.Operations[fmt.Sprintf("done-%02d", freshRuntimeJournalTerminalRetention+9)]; !found {
		t.Fatal("bounded journal dropped the newest terminal entry")
	}
	if _, found := loaded.Operations["done-00"]; found {
		t.Fatal("bounded journal retained the oldest terminal entry")
	}
}

// External must stay strictly status-only: it never becomes the Fresh
// lifecycle/strategy Runtime, even after ProductComplete.
func TestFreshRuntimeExternalConstructionKeepsRuntimeStatusOnly(t *testing.T) {
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	records := freshstore.New(opened.DB)
	if _, err := records.Update(t.Context(), opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(record *freshstore.Record) error {
		record.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: record.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	installed := validInstallationRuntime()
	routed, err := records.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.Update(t.Context(), routed.Operation.Identity, routed.Operation.Revision, func(record *freshstore.Record) error {
		record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: string(installed.Identity),
			TargetGeneration: uint64(installed.Generation), TargetArtifactID: string(installed.ActiveGatewayArtifact.ArtifactID),
		}
		record.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := records.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	opened.Record = completed

	app := newFreshApplication(opened, config.Config{}, nil)
	if app.runtime != nil {
		t.Fatal("External was injected as the Fresh lifecycle Runtime")
	}
	if app.runtimeExternal == nil {
		t.Fatal("External observation client was not constructed")
	}
	op := freshRuntimeProductCompleteOperation(t, app)
	status := httptest.NewRecorder()
	app.runtimeStatus(status, httptest.NewRequest(http.MethodGet, "/runtime/status", nil), op)
	var body freshRuntimeStatusResponse
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != string(model.RuntimeModeExternal) || body.ErrorCode != "runtime_unreachable" {
		t.Fatalf("External status = %+v", body)
	}
	submit := httptest.NewRecorder()
	app.runtimeOperations(submit, freshRuntimeRequest(t, "start", "external-1"), op, "runtime/operations")
	if submit.Code != http.StatusConflict {
		t.Fatalf("External start = %d body=%s", submit.Code, submit.Body.String())
	}
}

func freshRuntimeJournalOperation(t *testing.T, app *freshApplication, requestID string) freshRuntimeOperation {
	t.Helper()
	data, err := app.runtimeJournal.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	operation, found := data.Operations[requestID]
	if !found {
		t.Fatalf("runtime journal entry %q is missing", requestID)
	}
	return operation
}

// ageFreshRuntimeOperation simulates a Manager interruption: the entry was
// reserved in the past and no submitter is waiting on its own Runtime call.
func ageFreshRuntimeOperation(t *testing.T, app *freshApplication, requestID string, age time.Duration) {
	t.Helper()
	data, err := app.runtimeJournal.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	operation, found := data.Operations[requestID]
	if !found {
		t.Fatalf("runtime journal entry %q is missing", requestID)
	}
	operation.CreatedAt = operation.CreatedAt.Add(-age)
	operation.UpdatedAt = operation.UpdatedAt.Add(-age)
	data.Operations[requestID] = operation
	if err := app.runtimeJournal.save(t.Context(), data); err != nil {
		t.Fatal(err)
	}
}

func freshRuntimeGetOperation(app *freshApplication, op bootstrap.FreshOperation, operationID string) *httptest.ResponseRecorder {
	resource := "runtime/operations/" + operationID
	response := httptest.NewRecorder()
	app.runtimeOperations(response, httptest.NewRequest(http.MethodGet, "/"+resource, nil), op, resource)
	return response
}

// An UNKNOWN stop must recover from observation after a Manager interruption:
// the entry becomes terminal, the page reports the outcome, and the next Start
// is no longer locked out. The mutation itself is never resent.
func TestFreshRuntimeUnknownStopResolvesAndUnlocksStart(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeTestClient{
		status:  freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop),
		stopErr: errors.New("transport interrupted after acceptance"),
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)

	stop := httptest.NewRecorder()
	app.runtimeOperations(stop, freshRuntimeRequest(t, "stop", "stop-unknown-1"), op, "runtime/operations")
	if stop.Code != http.StatusAccepted {
		t.Fatalf("unknown stop = %d body=%s", stop.Code, stop.Body.String())
	}
	if entry := freshRuntimeJournalOperation(t, app, "stop-unknown-1"); entry.State != freshRuntimeStateUnknown {
		t.Fatalf("unknown stop entry = %+v", entry)
	}
	// A lost response must still adopt the lifecycle the user asked for so the
	// reconciler can converge on it.
	desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped {
		t.Fatalf("unknown stop desired lifecycle = %+v found=%v err=%v", desired, found, err)
	}

	blocked := httptest.NewRecorder()
	app.runtimeOperations(blocked, freshRuntimeRequest(t, "start", "start-blocked"), op, "runtime/operations")
	if blocked.Code != http.StatusConflict || client.starts != 0 {
		t.Fatalf("in-flight stop did not gate a start: %d starts=%d", blocked.Code, client.starts)
	}

	// The Supervisor finished the stop while the Manager was interrupted.
	ageFreshRuntimeOperation(t, app, "stop-unknown-1", freshRuntimeOperationUnknownBound/2)
	offline := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	offline.State = model.RuntimeStateOffline
	client.status = offline

	queried := freshRuntimeGetOperation(app, op, "runtime-ui/v1:stop-unknown-1")
	if queried.Code != http.StatusOK || !strings.Contains(queried.Body.String(), `"state":"succeeded"`) || client.stops != 1 {
		t.Fatalf("resolved stop = %d stops=%d body=%s", queried.Code, client.stops, queried.Body.String())
	}
	if entry := freshRuntimeJournalOperation(t, app, "stop-unknown-1"); entry.State != freshRuntimeStateSucceeded {
		t.Fatalf("resolved stop entry = %+v", entry)
	}

	start := httptest.NewRecorder()
	app.runtimeOperations(start, freshRuntimeRequest(t, "start", "start-after-unknown"), op, "runtime/operations")
	if start.Code != http.StatusOK || client.starts != 1 {
		t.Fatalf("start after an unknown stop = %d starts=%d body=%s", start.Code, client.starts, start.Body.String())
	}
}

// A lost Restart result restores the lifecycle it started from, so observation
// cannot confirm it: the bounded rule must expire it instead of blocking every
// other action forever.
func TestFreshRuntimeAbandonedRestartExpiresAtTheBound(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeTestClient{
		status:     freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart),
		restartErr: errors.New("transport interrupted after acceptance"),
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)

	restart := httptest.NewRecorder()
	app.runtimeOperations(restart, freshRuntimeRequest(t, "restart", "restart-unknown-1"), op, "runtime/operations")
	if restart.Code != http.StatusAccepted {
		t.Fatalf("unknown restart = %d body=%s", restart.Code, restart.Body.String())
	}
	ageFreshRuntimeOperation(t, app, "restart-unknown-1", freshRuntimeOperationUnknownBound)

	queried := freshRuntimeGetOperation(app, op, "runtime-ui/v1:restart-unknown-1")
	if queried.Code != http.StatusOK || !strings.Contains(queried.Body.String(), `"state":"failed"`) ||
		!strings.Contains(queried.Body.String(), `"failureCode":"unknown_expired"`) {
		t.Fatalf("expired restart = %d body=%s", queried.Code, queried.Body.String())
	}
	stop := httptest.NewRecorder()
	app.runtimeOperations(stop, freshRuntimeRequest(t, "stop", "stop-after-expiry"), op, "runtime/operations")
	if stop.Code != http.StatusOK || client.stops != 1 {
		t.Fatalf("stop after an expired restart = %d stops=%d body=%s", stop.Code, client.stops, stop.Body.String())
	}
}

type freshRuntimeBlockingClient struct {
	*freshRuntimeTestClient
	started chan struct{}
	release chan struct{}
}

func (c *freshRuntimeBlockingClient) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	close(c.started)
	<-c.release
	return freshRuntimeResult(request, model.RuntimeOperationStart, model.RuntimeOperationSucceeded), nil
}

type freshRuntimeContextAwareClient struct {
	*freshRuntimeTestClient
	started chan struct{}
	release chan struct{}
}

func (c *freshRuntimeContextAwareClient) Restart(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	close(c.started)
	select {
	case <-c.release:
		return freshRuntimeResult(request, model.RuntimeOperationRestart, model.RuntimeOperationSucceeded), nil
	case <-ctx.Done():
		return model.RuntimeOperationResult{}, ctx.Err()
	}
}

// A disconnected browser must not cancel the mutation's durable follow-up:
// the desired lifecycle and terminal journal result still need to be written.
func TestFreshRuntimeMutationPersistsAfterRequestCancellation(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeContextAwareClient{
		freshRuntimeTestClient: &freshRuntimeTestClient{
			status: freshRuntimeReadyStatus(model.RuntimeCapabilityRestart),
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)
	if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	submitRequest := freshRuntimeRequest(t, "restart", "restart-cancelled")
	submitRequest = submitRequest.WithContext(requestContext)
	submitted := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		app.runtimeOperations(response, submitRequest, op, "runtime/operations")
		submitted <- response
	}()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("restart mutation never reached the Runtime client")
	}
	cancel()
	close(client.release)

	select {
	case response := <-submitted:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"succeeded"`) {
			t.Fatalf("cancelled restart = %d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled restart never completed")
	}
	entry := freshRuntimeJournalOperation(t, app, "restart-cancelled")
	if entry.State != freshRuntimeStateSucceeded {
		t.Fatalf("cancelled restart entry = %+v", entry)
	}
	desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
		t.Fatalf("cancelled restart desired lifecycle = %+v found=%v err=%v", desired, found, err)
	}
}

// Operation polling must answer while a lifecycle mutation is in flight: the
// journal mutex is never held across the Runtime network call.
func TestFreshRuntimePollAnswersWhileMutationIsInFlight(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeBlockingClient{
		freshRuntimeTestClient: &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)},
		started:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)
	submitRequest := freshRuntimeRequest(t, "start", "start-slow")

	submitted := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		app.runtimeOperations(response, submitRequest, op, "runtime/operations")
		submitted <- response
	}()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("start mutation never reached the Runtime client")
	}

	polled := make(chan *httptest.ResponseRecorder, 1)
	go func() { polled <- freshRuntimeGetOperation(app, op, "runtime-ui/v1:start-slow") }()
	select {
	case response := <-polled:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"accepted"`) {
			t.Fatalf("in-flight poll = %d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("operation poll blocked while the lifecycle mutation was in flight")
	}
	select {
	case <-submitted:
		t.Fatal("the mutation returned before the test released it")
	default:
	}

	close(client.release)
	select {
	case response := <-submitted:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"succeeded"`) {
			t.Fatalf("released start = %d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start mutation never completed")
	}
	if entry := freshRuntimeJournalOperation(t, app, "start-slow"); entry.State != freshRuntimeStateSucceeded {
		t.Fatalf("final entry = %+v", entry)
	}
}

// A terminal failed Supervisor result is not evidence that the requested lifecycle
// was reached, so it must not move the reconciler's desired lifecycle.
func TestFreshRuntimeFailedResultDoesNotFlipDesiredLifecycle(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	client := &freshRuntimeTestClient{
		status: freshRuntimeReadyStatus(model.RuntimeCapabilityStop),
		stopResult: model.RuntimeOperationResult{
			OperationID:       "runtime-ui/v1:stop-failed",
			OperationType:     model.RuntimeOperationStop,
			RuntimeIdentity:   "runtime-test",
			RuntimeGeneration: 7,
			State:             model.RuntimeOperationFailed,
			Failure:           &model.RuntimeOperationFailure{Code: "gateway_stop_failed"},
		},
	}
	app.runtime = client
	app.runtimeExternal = nil
	op := freshRuntimeProductCompleteOperation(t, app)

	before, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning)
	if err != nil {
		t.Fatal(err)
	}
	failed := httptest.NewRecorder()
	app.runtimeOperations(failed, freshRuntimeRequest(t, "stop", "stop-failed"), op, "runtime/operations")
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"state":"failed"`) ||
		!strings.Contains(failed.Body.String(), `"failureCode":"gateway_stop_failed"`) {
		t.Fatalf("failed stop = %d body=%s", failed.Code, failed.Body.String())
	}
	entry := freshRuntimeJournalOperation(t, app, "stop-failed")
	if entry.State != freshRuntimeStateFailed || entry.FailureCode != "gateway_stop_failed" {
		t.Fatalf("failed stop entry = %+v", entry)
	}
	after, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || after.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning || after.Revision != before.Revision {
		t.Fatalf("failed stop changed desired lifecycle: before=%+v after=%+v found=%v err=%v", before, after, found, err)
	}
}

// A Supervisor rejection happens before any side effect, so the entry must end
// up terminal and the desired lifecycle must not move.
func TestFreshRuntimeSupervisorConflictEndsEntryWithoutFlippingDesiredLifecycle(t *testing.T) {
	for _, testCase := range []struct {
		name string
		code runtimeservice.ProtocolErrorCode
		want string
	}{
		{name: "state conflict", code: runtimeservice.ProtocolErrorOperationStateConflict, want: "operation_conflict"},
		{name: "operation ID conflict", code: runtimeservice.ProtocolErrorOperationIDConflict, want: "operation_id_conflict"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app, closeApp := newA3TestApp(t)
			defer closeApp()
			client := &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)}
			app.runtime = client
			app.runtimeExternal = nil
			op := freshRuntimeProductCompleteOperation(t, app)
			before, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning)
			if err != nil {
				t.Fatal(err)
			}
			client.stopErr = &runtimeservice.ProtocolError{Code: testCase.code, HTTPStatus: http.StatusConflict}

			rejected := httptest.NewRecorder()
			app.runtimeOperations(rejected, freshRuntimeRequest(t, "stop", "stop-rejected"), op, "runtime/operations")
			if rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), testCase.want) {
				t.Fatalf("rejected stop = %d body=%s", rejected.Code, rejected.Body.String())
			}
			entry := freshRuntimeJournalOperation(t, app, "stop-rejected")
			if entry.State != freshRuntimeStateFailed || entry.FailureCode != testCase.want {
				t.Fatalf("rejected stop entry = %+v", entry)
			}
			after, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
			if err != nil || !found || after.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning || after.Revision != before.Revision {
				t.Fatalf("rejected stop changed the desired lifecycle: before=%+v after=%+v found=%v err=%v", before, after, found, err)
			}
			start := httptest.NewRecorder()
			app.runtimeOperations(start, freshRuntimeRequest(t, "start", "start-after-rejection"), op, "runtime/operations")
			if start.Code != http.StatusOK {
				t.Fatalf("start after a rejected stop = %d body=%s", start.Code, start.Body.String())
			}
		})
	}
}
