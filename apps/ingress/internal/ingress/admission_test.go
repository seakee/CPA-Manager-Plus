package ingress

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func committedProjection() FreshRouteState {
	return FreshRouteState{InstallationID: "install-one", Generation: "generation-one", OperationID: "fresh-operation", Revision: 3, Target: "/control", Committed: true}
}

func admittedReply(state FreshRouteState) FreshAuthority {
	return FreshAuthority{
		FreshRouteState: state,
		GatewayAdmission: &GatewayAdmission{
			InstallationID: state.InstallationID,
			Generation:     state.Generation,
			Admitted:       true,
		},
	}
}

func TestFreshAuthorityAdmissionRequiresConsistentCommittedBinding(t *testing.T) {
	state := committedProjection()
	pending := state
	pending.Committed = false
	mutate := func(change func(*FreshAuthority)) func() FreshAuthority {
		return func() FreshAuthority {
			reply := admittedReply(state)
			change(&reply)
			return reply
		}
	}
	for _, test := range []struct {
		name    string
		reply   func() FreshAuthority
		current FreshRouteState
		want    bool
	}{
		{name: "positive", reply: func() FreshAuthority { return admittedReply(state) }, current: state, want: true},
		{name: "older-manager-without-admission", reply: func() FreshAuthority { return FreshAuthority{FreshRouteState: state} }, current: state},
		{name: "manager-denied", reply: mutate(func(r *FreshAuthority) { r.GatewayAdmission.Admitted = false }), current: state},
		{name: "admission-installation-mismatch", reply: mutate(func(r *FreshAuthority) { r.GatewayAdmission.InstallationID = "other-install" }), current: state},
		{name: "admission-generation-mismatch", reply: mutate(func(r *FreshAuthority) { r.GatewayAdmission.Generation = "other-generation" }), current: state},
		{name: "projection-target-mismatch", reply: mutate(func(r *FreshAuthority) { r.Target = "/other-control" }), current: state},
		{name: "projection-revision-mismatch", reply: mutate(func(r *FreshAuthority) { r.Revision++ }), current: state},
		{name: "projection-not-committed", reply: func() FreshAuthority { return admittedReply(pending) }, current: pending},
		{name: "empty-projection", reply: func() FreshAuthority { return admittedReply(FreshRouteState{}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			admission, ok := test.reply().AdmittedFor(test.current)
			if ok != test.want {
				t.Fatalf("AdmittedFor = %v, want %v", ok, test.want)
			}
			if ok && (admission.InstallationID != test.current.InstallationID || admission.Generation != test.current.Generation) {
				t.Fatalf("admission is not bound to the projection: %+v", admission)
			}
		})
	}
}

func TestGatewayAdmitterRequiresPositiveAnswerAndReconfirmsAfterTTL(t *testing.T) {
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	var answer atomic.Value
	answer.Store(admittedReply(routes.State()))
	var queries atomic.Int32
	var fail atomic.Bool
	admitter := NewGatewayAdmitter(routes, func(context.Context) (FreshAuthority, error) {
		queries.Add(1)
		if fail.Load() {
			return FreshAuthority{}, errors.New("Manager unreachable")
		}
		return answer.Load().(FreshAuthority), nil
	}, 15*time.Second)
	now := time.Unix(1_700_000_000, 0)
	admitter.now = func() time.Time { return now }

	if !admitter.Admitted(t.Context()) {
		t.Fatal("positive Manager answer not admitted")
	}
	// One positive answer is reused inside its TTL.
	now = now.Add(14 * time.Second)
	if !admitter.Admitted(t.Context()) || queries.Load() != 1 {
		t.Fatalf("positive answer not cached: queries=%d", queries.Load())
	}
	// TTL expiry re-reads Manager; a withdrawn answer closes the Gateway.
	answer.Store(FreshAuthority{FreshRouteState: routes.State()})
	now = now.Add(2 * time.Second)
	if admitter.Admitted(t.Context()) || queries.Load() != 2 {
		t.Fatalf("expired answer still admitted: queries=%d", queries.Load())
	}
	// The negative answer is also bounded, then a fresh positive reopens.
	answer.Store(admittedReply(routes.State()))
	now = now.Add(DefaultGatewayAdmissionDeniedTTL)
	if !admitter.Admitted(t.Context()) {
		t.Fatal("recovered Manager answer not admitted")
	}
	// An unreachable Manager never admits.
	now = now.Add(time.Minute)
	fail.Store(true)
	if admitter.Admitted(t.Context()) {
		t.Fatal("Manager outage admitted Gateway traffic")
	}
}

func TestGatewayAdmitterWithdrawsWhenProjectionChanges(t *testing.T) {
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	var queries atomic.Int32
	admitter := NewGatewayAdmitter(routes, func(context.Context) (FreshAuthority, error) {
		queries.Add(1)
		return FreshAuthority{FreshRouteState: routes.State()}, nil
	}, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	admitter.now = func() time.Time { return now }
	if admitter.Admitted(t.Context()) {
		t.Fatal("projection with a non-admitting Manager answer was admitted")
	}
	// A negative answer is cached, so the second read stays closed.
	if admitter.Admitted(t.Context()) || queries.Load() != 1 {
		t.Fatalf("negative answer not cached: queries=%d", queries.Load())
	}
	// Losing the projection closes immediately, before any TTL.
	routes.state = FreshRouteState{}
	now = now.Add(2 * DefaultGatewayAdmissionDeniedTTL)
	if admitter.Admitted(t.Context()) || queries.Load() != 1 {
		t.Fatal("absent projection admitted Gateway traffic")
	}
}

func TestGatewayAdmitterNeverAdmitsWithoutLiveInputs(t *testing.T) {
	query := func(context.Context) (FreshAuthority, error) {
		return admittedReply(committedProjection()), nil
	}
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	if (*GatewayAdmitter)(nil).Admitted(t.Context()) {
		t.Fatal("nil admitter admitted")
	}
	if NewGatewayAdmitter(nil, query, time.Minute).Admitted(t.Context()) {
		t.Fatal("admitter without routes admitted")
	}
	if NewGatewayAdmitter(routes, nil, time.Minute).Admitted(t.Context()) {
		t.Fatal("admitter without Manager query admitted")
	}
	if NewGatewayAdmitter(routes, query, 0).Admitted(t.Context()) {
		t.Fatal("admitter without a bounded TTL admitted")
	}
	if got := NewGatewayAdmitter(routes, query, time.Minute).queryTimeout; got != DefaultGatewayAdmissionQueryTimeout {
		t.Fatalf("Manager read bound = %s, want %s", got, DefaultGatewayAdmissionQueryTimeout)
	}
}

func TestCancelledCallerNeverDeniesTheNextCaller(t *testing.T) {
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	var attempts atomic.Int32
	var queryCtxCancelled atomic.Bool
	query := func(ctx context.Context) (FreshAuthority, error) {
		if attempts.Add(1) == 1 {
			// The triggering client leaves while the shared refresh is running.
			cancelCaller()
			time.Sleep(10 * time.Millisecond)
			queryCtxCancelled.Store(ctx.Err() != nil)
			return FreshAuthority{}, errors.New("Manager unavailable")
		}
		return admittedReply(routes.State()), nil
	}
	admitter := NewGatewayAdmitter(routes, query, time.Minute)
	admitter.queryTimeout = 200 * time.Millisecond
	if admitter.Admitted(callerCtx) {
		t.Fatal("failed Manager read admitted Gateway traffic")
	}
	if queryCtxCancelled.Load() {
		t.Fatal("the shared refresh inherited the departed caller's context")
	}
	// The abandoned attempt must not have cached a denial: the next caller
	// re-reads Manager immediately and is admitted.
	if !admitter.Admitted(t.Context()) || attempts.Load() != 2 {
		t.Fatalf("departed caller closed the Gateway for the next caller: attempts=%d", attempts.Load())
	}
}

func TestSlowManagerRefreshStaysBounded(t *testing.T) {
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	var attempts atomic.Int32
	query := func(ctx context.Context) (FreshAuthority, error) {
		attempts.Add(1)
		<-ctx.Done()
		return FreshAuthority{}, ctx.Err()
	}
	admitter := NewGatewayAdmitter(routes, query, time.Minute)
	admitter.queryTimeout = 30 * time.Millisecond
	if admitter.queryTimeout != 30*time.Millisecond {
		t.Fatal("test did not bound the Manager read")
	}
	started := time.Now()
	if admitter.Admitted(t.Context()) {
		t.Fatal("hung Manager read admitted Gateway traffic")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung Manager read was not bounded: %s", elapsed)
	}
	if attempts.Load() != 1 {
		t.Fatalf("hung Manager read attempted %d times", attempts.Load())
	}
}

func TestHandlerAdmitsFreshGatewayTrafficOnlyWhileManagerAdmits(t *testing.T) {
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer manager.Close()
	var gatewayHits atomic.Int32
	var authorization atomic.Value
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		authorization.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	managerURL, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	gatewayURL, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	reply := &atomic.Pointer[FreshAuthority]{}
	initial := admittedReply(committedProjection())
	reply.Store(&initial)
	var failing atomic.Bool
	var queries atomic.Int32
	admitter := NewGatewayAdmitter(routes, func(context.Context) (FreshAuthority, error) {
		queries.Add(1)
		if failing.Load() {
			return FreshAuthority{}, errors.New("Manager unreachable")
		}
		return *reply.Load(), nil
	}, 15*time.Second)
	now := time.Unix(1_700_000_000, 0)
	admitter.now = func() time.Time { return now }
	handler := NewFreshHandler(routes, managerURL, gatewayURL, log.New(io.Discard, "", 0), nil)
	handler.EnableFreshGatewayAdmission(admitter.Admitted)
	request := func() int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer cpamp_sk_client")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}

	if code := request(); code != http.StatusOK || gatewayHits.Load() != 1 || authorization.Load() != "Bearer cpamp_sk_client" {
		t.Fatalf("admitted request status=%d hits=%d auth=%v", code, gatewayHits.Load(), authorization.Load())
	}
	// The positive answer survives its TTL without another Manager read.
	now = now.Add(14 * time.Second)
	if code := request(); code != http.StatusOK || queries.Load() != 1 {
		t.Fatalf("cached admission status=%d queries=%d", code, queries.Load())
	}
	// TTL expiry re-reads Manager; a withdrawn answer closes the Gateway.
	withdrawn := FreshAuthority{FreshRouteState: committedProjection()}
	reply.Store(&withdrawn)
	now = now.Add(2 * time.Second)
	if code := request(); code != http.StatusServiceUnavailable || gatewayHits.Load() != 2 {
		t.Fatalf("withdrawn admission forwarded: status=%d hits=%d", code, gatewayHits.Load())
	}
	// The negative answer is bounded, then a fresh positive reopens Gateway.
	reply.Store(&initial)
	now = now.Add(DefaultGatewayAdmissionDeniedTTL)
	if code := request(); code != http.StatusOK || gatewayHits.Load() != 3 {
		t.Fatalf("recovered admission status=%d hits=%d", code, gatewayHits.Load())
	}
	// A projection that no longer matches the Manager answer closes at once,
	// without waiting for the TTL.
	changed := committedProjection()
	changed.Generation = "generation-two"
	routes.state = changed
	if code := request(); code != http.StatusServiceUnavailable || gatewayHits.Load() != 3 {
		t.Fatalf("changed projection forwarded: status=%d hits=%d", code, gatewayHits.Load())
	}
	// An unreachable Manager never admits.
	routes.state = committedProjection()
	failing.Store(true)
	now = now.Add(2 * DefaultGatewayAdmissionDeniedTTL)
	if code := request(); code != http.StatusServiceUnavailable || gatewayHits.Load() != 3 {
		t.Fatalf("Manager outage forwarded: status=%d hits=%d", code, gatewayHits.Load())
	}
	// Losing the projection closes Gateway even with a cached positive answer.
	failing.Store(false)
	now = now.Add(2 * DefaultGatewayAdmissionDeniedTTL)
	if code := request(); code != http.StatusOK {
		t.Fatalf("restored projection status=%d", code)
	}
	routes.state = FreshRouteState{}
	if code := request(); code != http.StatusServiceUnavailable || gatewayHits.Load() != 4 {
		t.Fatalf("absent projection forwarded: status=%d hits=%d", code, gatewayHits.Load())
	}
}

func TestHandlerAdmissionNeverForwardsManagementOrPrivatePaths(t *testing.T) {
	var managerPaths, gatewayHits atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		managerPaths.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer manager.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	managerURL, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	gatewayURL, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	routes := &FreshRoutes{}
	routes.state = committedProjection()
	handler := NewFreshHandler(routes, managerURL, gatewayURL, log.New(io.Discard, "", 0), nil)
	handler.EnableFreshGatewayAdmission(func(context.Context) bool { return true })

	for _, test := range []struct {
		target string
		want   int
	}{
		{target: "/v0/management/config", want: http.StatusNoContent},
		{target: "/management.html", want: http.StatusNoContent},
		{target: "/usage-service/config", want: http.StatusNoContent},
		{target: "/v8/management/config", want: http.StatusNotFound},
		{target: "/v8/management/config/access/api-keys", want: http.StatusNotFound},
		{target: "/v8/management/config/api-keys", want: http.StatusNotFound},
		{target: "/v8/management/config/api-keys/claude", want: http.StatusNotFound},
		{target: "/v8%2fmanagement%2fconfig", want: http.StatusNotFound},
		{target: FreshRoutingAuthorityPath, want: http.StatusNotFound},
		{target: FreshRoutingControlPath, want: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, test.target, nil)
		req.Header.Set("Authorization", "Bearer cpa-management-looking-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != test.want {
			t.Fatalf("%s status = %d, want %d", test.target, w.Code, test.want)
		}
	}
	if gatewayHits.Load() != 0 {
		t.Fatalf("management/private path reached Gateway: %d", gatewayHits.Load())
	}
	if managerPaths.Load() != 3 {
		t.Fatalf("manager-owned paths = %d, want 3", managerPaths.Load())
	}
	// The same handler still forwards ordinary model traffic once admitted.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || gatewayHits.Load() != 1 {
		t.Fatalf("model traffic status=%d hits=%d", w.Code, gatewayHits.Load())
	}
}
